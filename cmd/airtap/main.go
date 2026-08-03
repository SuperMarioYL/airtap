// Package main implements the airtap thin client. It is the laptop-side
// entrypoint that loads an airtap.yaml EgressManifest, bootstraps mTLS
// material via `airtap init`, and streams the on-box agent loop's output
// to the terminal over a mutual-TLS tunnel. The model + tools run on the
// GPU box inside airtapd; the thin client only carries the prompt in and
// the streamed log out — 数据不出境 by construction.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/SuperMarioYL/airtap/internal/manifest"
	"github.com/SuperMarioYL/airtap/internal/tunnel"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// log is the shared thin-client logger. It writes structured JSON to
// stderr so stdout stays clean for the streamed agent log (tui.Stream).
var log = zerolog.New(os.Stderr).With().Timestamp().Logger()

// version is the thin-client version. Overridden at release time by the
// goreleaser ldflags `-X main.version=<git tag>` (feat-goreleaser-prebuilt-
// binary); defaults to "dev" for local `go build` so a bare binary still
// reports something via `airtap --version`.
var version = "dev"

// rootCmd is the `airtap` CLI root. Subcommands are wired in init() so
// each handler file can own its own command + flags.
var rootCmd = &cobra.Command{
	Use:   "airtap",
	Short: "Airtap thin client — drive a 信创 GPU box agent over mTLS",
	Long: `Airtap is the thin-client side of the Airtap m1 MVP.

It loads an airtap.yaml EgressManifest, dials the box over mutual TLS, and
streams the on-box agent loop's tool calls + diffs to the terminal. All
model traffic stays on the box; only the streamed log crosses the tunnel.

Subcommands:
  init     Generate the CA + client/daemon leaf and a starter airtap.yaml
  connect  Open a raw mTLS stream to the box (stdin <-> conn <-> stdout)
  run      Run a prompt on the box and stream the agent log to the TUI
  audit    Print the egress audit trail from the manifest's egress.audit`,
}

// starterManifest is the working airtap.yaml `airtap init` writes when one
// does not already exist. It mirrors examples/airtap.yaml but defaults to
// a local loopback box so a fresh `init && airtapd` just works.
const starterManifest = `# Airtap EgressManifest — the contract shared (out of band) between the
# thin client and airtapd. Edit box.addr to point at your GPU box.
box:
  addr: 127.0.0.1:7437   # host:port of the box's airtapd mTLS listener
  tls: mTLS              # v0.1 only supports mutual TLS
  ca: ./ca.pem            # CA used to verify the peer's leaf cert
model:
  endpoint: http://127.0.0.1:8000/v1  # on-box OpenAI-compatible endpoint
  name: deepseek-v3
egress:
  allow:                  # sole permitted dial targets (host:port)
    - 127.0.0.1:8000
  mode: airgap            # airgap: deny everything not in allow
  audit: ./audit.log      # append-only egress trail
agent:
  workdir: .              # repo root the agent edits (chdir target)
  tools: [read, write, list, bash]
`

func init() {
	// init: output directory for the CA + leaf + starter manifest.
	initCmd.Flags().StringVarP(&initFlags.out, "out", "o", ".", "output directory for mTLS material + starter manifest")

	// connect: same mTLS material flags as run, since it also dials the box.
	connectCmd.Flags().StringVarP(&connectFlags.manifest, "manifest", "m", "./airtap.yaml", "path to airtap.yaml EgressManifest")
	connectCmd.Flags().StringVar(&connectFlags.cert, "cert", "./airtap.crt", "client leaf cert (mTLS)")
	connectCmd.Flags().StringVar(&connectFlags.key, "key", "./airtap.key", "client leaf key (mTLS)")
	connectCmd.Flags().StringVar(&connectFlags.ca, "ca", "", "CA pem (defaults to manifest box.ca)")

	// audit: read the trail from the manifest's egress.audit, or --file.
	auditCmd.Flags().StringVarP(&auditFlags.manifest, "manifest", "m", "./airtap.yaml", "path to airtap.yaml EgressManifest")
	auditCmd.Flags().StringVarP(&auditFlags.file, "file", "f", "", "audit log path (defaults to manifest egress.audit)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(auditCmd)

	// feat-goreleaser-prebuilt-binary: surface the release-injected version.
	rootCmd.Version = version
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		// cobra already printed the error; exit non-zero.
		os.Exit(1)
	}
}

// --- airtap init ------------------------------------------------------------

var initFlags struct {
	out string
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate the CA + client/daemon leaf and a starter airtap.yaml",
	Long: `init bootstraps the mTLS material a thin client and airtapd both need:

  ca.pem / ca.key      the trust anchor (generated once, kept private)
  airtap.crt / .key    a single leaf usable as BOTH the client cert (laptop)
                       and the daemon's server cert (box), per tunnel.GenClientCert

If ca.pem already exists it is reused so existing leaves stay valid. A
starter airtap.yaml is written only when one is not already present.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := initFlags.out
		caCertPath := filepath.Join(dir, "ca.pem")
		caKeyPath := filepath.Join(dir, "ca.key")

		var caCertPEM, caKeyPEM []byte
		if _, err := os.Stat(caCertPath); err == nil {
			// Reuse the existing CA so we don't invalidate prior leaves.
			if caCertPEM, err = os.ReadFile(caCertPath); err != nil {
				return fmt.Errorf("init: read ca.pem: %w", err)
			}
			if caKeyPEM, err = os.ReadFile(caKeyPath); err != nil {
				return fmt.Errorf("init: read ca.key: %w", err)
			}
			log.Info().Str("path", caCertPath).Msg("init: reusing existing CA")
		} else {
			c, k, err := tunnel.GenCA()
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			caCertPEM, caKeyPEM = c, k
			if err := os.WriteFile(caCertPath, caCertPEM, 0o644); err != nil {
				return fmt.Errorf("init: write ca.pem: %w", err)
			}
			if err := os.WriteFile(caKeyPath, caKeyPEM, 0o600); err != nil {
				return fmt.Errorf("init: write ca.key: %w", err)
			}
			log.Info().Str("path", caCertPath).Msg("init: generated CA")
		}

		// One leaf serves as both the client cert (laptop) and the daemon's
		// server cert (box) — tunnel.GenClientCert mints it with both EKUs.
		leafCert, leafKey, err := tunnel.GenClientCert(caCertPEM, caKeyPEM)
		if err != nil {
			return fmt.Errorf("init: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "airtap.crt"), leafCert, 0o644); err != nil {
			return fmt.Errorf("init: write airtap.crt: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "airtap.key"), leafKey, 0o600); err != nil {
			return fmt.Errorf("init: write airtap.key: %w", err)
		}
		log.Info().Str("path", filepath.Join(dir, "airtap.crt")).Msg("init: generated leaf")

		manifestPath := filepath.Join(dir, "airtap.yaml")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			if err := os.WriteFile(manifestPath, []byte(starterManifest), 0o644); err != nil {
				return fmt.Errorf("init: write airtap.yaml: %w", err)
			}
			log.Info().Str("path", manifestPath).Msg("init: wrote starter manifest")
		} else {
			log.Info().Str("path", manifestPath).Msg("init: manifest already present, left untouched")
		}

		log.Info().Str("dir", dir).Msg("init: complete")
		// fix-bash-tool-egress-bypass: surface the netns requirement via the
		// init path so the operator prepares the GPU box (where airtapd runs)
		// before the first `airtap run`. The bash tool isolates subprocesses in
		// a CLONE_NEWNET netns; on a box that cannot create one (no CAP_SYS_ADMIN
		// and unprivileged userns disabled) the bash tool fail-closes.
		log.Info().Str("hint", "on the GPU box running airtapd: grant CAP_SYS_ADMIN (root) or enable unprivileged userns so the bash tool can isolate subprocesses (CLONE_NEWNET) — without it bash calls fail-closed to preserve 数据不出境").
			Msg("init: netns requirement")
		return nil
	},
}

// --- airtap connect ---------------------------------------------------------

var connectFlags struct {
	manifest string
	cert     string
	key      string
	ca       string
}

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Open a raw mTLS stream to the box (stdin <-> conn <-> stdout)",
	Long: `connect dials the box's mTLS listener and pipes stdin to the box and
the box to stdout, line by line. It is the debug/introspection surface for
operators who want a raw tunnel without running the agent loop.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		m, err := manifest.Load(connectFlags.manifest)
		if err != nil {
			return err
		}
		caPath := connectFlags.ca
		if caPath == "" {
			caPath = m.Box.CA
		}
		conn, err := tunnel.Dial(m.Box.Addr, caPath, connectFlags.cert, connectFlags.key)
		if err != nil {
			return err
		}
		defer conn.Close()
		log.Info().Str("addr", m.Box.Addr).Msg("connect: mTLS established")

		// Cancel on SIGINT so a Ctrl-C tears down both copy goroutines.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		errc := make(chan error, 2)
		go func() { _, err := io.Copy(os.Stdout, conn); errc <- err }()
		go func() { _, err := io.Copy(conn, os.Stdin); errc <- err }()

		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		}
	},
}

// --- airtap audit ------------------------------------------------------------

var auditFlags struct {
	manifest string
	file     string
}

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Print the egress audit trail",
	Long: `audit prints the append-only egress trail. By default it reads the path
named in the manifest's egress.audit; pass --file to print any other log
file. On the box this is the tamper-evident-by-append record of every
outbound dial (allowed or denied) the daemon made.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := auditFlags.file
		if path == "" {
			m, err := manifest.Load(auditFlags.manifest)
			if err != nil {
				return err
			}
			path = m.Egress.Audit
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("audit: open %s: %w", path, err)
		}
		defer f.Close()
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return fmt.Errorf("audit: read %s: %w", path, err)
		}
		return nil
	},
}
