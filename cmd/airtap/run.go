// run.go hosts the `airtap run <prompt>` subcommand. It is the thin
// client's main entrypoint: load the manifest, dial the box over mTLS,
// send the prompt, and stream the agent loop's progress lines (tool
// calls, diffs, final answer) into tui.Stream. The ReAct loop itself
// lives on the box inside airtapd; this file only carries the prompt in
// and renders the streamed log out.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/SuperMarioYL/airtap/internal/manifest"
	"github.com/SuperMarioYL/airtap/internal/tui"
	"github.com/SuperMarioYL/airtap/internal/tunnel"
	"github.com/spf13/cobra"
)

// runFlags holds the flags for `airtap run`. cert/key are the laptop's
// client leaf (minted by `airtap init`); ca defaults to the manifest's
// box.ca so the operator only edits one place.
var runFlags struct {
	manifest string
	cert     string
	key      string
	ca       string
}

// runCmd is the `airtap run <prompt>` cobra command. It is registered on
// rootCmd in main.go's init().
var runCmd = &cobra.Command{
	Use:   "run <prompt>",
	Short: "Run a prompt on the box and stream the agent log to the terminal",
	Long: `run loads the EgressManifest, dials the box over mTLS, sends the prompt
as the first line of the tunnel, and renders every line the box streams
back through tui.Stream — tool calls, tool results, and the final answer.

The agent loop, model client, and egress proxy all run on the box inside
airtapd; the thin client is just the prompt in / log out carrier.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt := strings.Join(args, " ")

		m, err := manifest.Load(runFlags.manifest)
		if err != nil {
			return err
		}
		caPath := runFlags.ca
		if caPath == "" {
			caPath = m.Box.CA
		}

		// Honor Ctrl-C so a long run can be torn down; closing the conn
		// also signals airtapd that the client went away.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		conn, err := tunnel.Dial(m.Box.Addr, caPath, runFlags.cert, runFlags.key)
		if err != nil {
			return err
		}
		defer conn.Close()
		log.Info().Str("addr", m.Box.Addr).Str("model", m.Model.Name).Msg("run: mTLS established")

		// The daemon reads the first line as the task prompt.
		if _, err := io.WriteString(conn, prompt+"\n"); err != nil {
			return fmt.Errorf("run: send prompt: %w", err)
		}

		stream := tui.NewStream()
		defer stream.Close()

		// Render every line the box streams back. The agent loop writes
		// newline-terminated progress lines over this same conn, so a
		// line scanner is the natural framing.
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if err := stream.Render(line); err != nil {
				return fmt.Errorf("run: render: %w", err)
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			return fmt.Errorf("run: stream: %w", err)
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringVarP(&runFlags.manifest, "manifest", "m", "./airtap.yaml", "path to airtap.yaml EgressManifest")
	runCmd.Flags().StringVar(&runFlags.cert, "cert", "./airtap.crt", "client leaf cert (mTLS)")
	runCmd.Flags().StringVar(&runFlags.key, "key", "./airtap.key", "client leaf key (mTLS)")
	runCmd.Flags().StringVar(&runFlags.ca, "ca", "", "CA pem (defaults to manifest box.ca)")
}
