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
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"

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
		// closeConn is shared by the deferred close and the runStream
		// cancellation goroutine; sync.Once guards against double-close on
		// the normal-return path (fix-airtap-run-ctrl-c-blocked).
		var closeOnce sync.Once
		closeConn := func() { closeOnce.Do(func() { conn.Close() }) }
		defer closeConn()
		log.Info().Str("addr", m.Box.Addr).Str("model", m.Model.Name).Msg("run: mTLS established")

		// The daemon reads the first line as the task prompt.
		if _, err := io.WriteString(conn, prompt+"\n"); err != nil {
			return fmt.Errorf("run: send prompt: %w", err)
		}

		stream := tui.NewStream()
		defer stream.Close()

		return runStream(ctx, conn, closeConn, stream)
	},
}

// runStream renders every line the box streams back over conn. The agent loop
// writes newline-terminated progress lines over the same conn, so a line
// scanner is the natural framing.
//
// fix-airtap-run-ctrl-c-blocked: scanner.Scan blocks in conn.Read and only
// checked ctx.Err() after a line arrived, so during a quiet model-generation
// period (30s+ with no streamed lines) Ctrl-C could not exit until the next
// line or EOF. A goroutine now closes conn on ctx.Done so the blocking Read
// returns (EOF / err-closed) and the loop unblocks promptly. closeConn
// (sync.Once-guarded) is shared with the caller's deferred close to avoid a
// double-close on the normal-return path.
func runStream(ctx context.Context, conn net.Conn, closeConn func(), stream *tui.Stream) error {
	go func() {
		<-ctx.Done()
		closeConn()
	}()

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
}

func init() {
	runCmd.Flags().StringVarP(&runFlags.manifest, "manifest", "m", "./airtap.yaml", "path to airtap.yaml EgressManifest")
	runCmd.Flags().StringVar(&runFlags.cert, "cert", "./airtap.crt", "client leaf cert (mTLS)")
	runCmd.Flags().StringVar(&runFlags.key, "key", "./airtap.key", "client leaf key (mTLS)")
	runCmd.Flags().StringVar(&runFlags.ca, "ca", "", "CA pem (defaults to manifest box.ca)")
}
