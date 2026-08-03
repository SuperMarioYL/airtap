// Package main implements airtapd, the on-box daemon. It owns the mTLS
// listener, the egress proxy (the sole network egress point of the agent
// process), the audit trail, and the ReAct agent loop. On each accepted
// thin-client connection it reads the prompt as the first line, points
// the loop's progress sink at the connection, and runs the loop — so tool
// calls + diffs stream back over the tunnel to the laptop while the model
// traffic stays local to 127.0.0.1 on the box.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/SuperMarioYL/airtap/internal/agent"
	"github.com/SuperMarioYL/airtap/internal/audit"
	"github.com/SuperMarioYL/airtap/internal/egress"
	"github.com/SuperMarioYL/airtap/internal/manifest"
	"github.com/SuperMarioYL/airtap/internal/model"
	"github.com/SuperMarioYL/airtap/internal/tunnel"
	"github.com/rs/zerolog"
)

// log writes structured JSON to stderr so the connection's stdout stream
// (the agent loop progress) is never contaminated by daemon chatter.
var log = zerolog.New(os.Stderr).With().Timestamp().Logger()

// loopMu serializes loop runs. The agent Loop holds a single output sink
// (l.out) set via SetOutput; concurrent connections would otherwise
// clobber each other's sink. MVP is single-operator, so queueing is fine.
var loopMu sync.Mutex

func main() {
	manifestPath := flag.String("manifest", "./airtap.yaml", "path to airtap.yaml EgressManifest")
	certPath := flag.String("cert", "./airtap.crt", "server leaf cert (mTLS)")
	keyPath := flag.String("key", "./airtap.key", "server leaf key (mTLS)")
	flag.Parse()

	m, err := manifest.Load(*manifestPath)
	if err != nil {
		fail(err)
	}
	log.Info().
		Str("addr", m.Box.Addr).
		Str("model", m.Model.Name).
		Str("egress", m.Egress.Mode).
		Msg("airtapd: manifest loaded")

	// chdir into the agent workdir so the read/write/list/bash tools
	// operate relative to the repo root the agent is meant to edit.
	if err := os.Chdir(m.Agent.Workdir); err != nil {
		fail(fmt.Errorf("airtapd: chdir %s: %w", m.Agent.Workdir, err))
	}

	// The audit trail is the single append-only record of every outbound
	// dial attempt; the egress proxy records to it on every allow/deny.
	auditLog, err := audit.New(m.Egress.Audit)
	if err != nil {
		fail(err)
	}
	defer auditLog.Close()

	// The egress proxy is the sole network egress point. NewLoop wires
	// it into http.DefaultTransport.DialContext below, so the model
	// client's HTTP calls (and any other outbound dial) are gated here.
	proxy := egress.NewProxy(m.Egress.Allow, auditLog)
	client := model.NewClient(m.Model.Endpoint, m.Model.Name)
	loop := agent.NewLoop(m, client, proxy, auditLog)

	// fix-bash-tool-egress-bypass: the bash tool isolates subprocesses in a
	// CLONE_NEWNET netns (loopback-only) so raw-socket tools cannot dial off
	// the box. Surface the CAP_SYS_ADMIN / unprivileged-userns requirement at
	// startup so the operator can fix the kernel BEFORE the first `airtap run`
	// fails closed. airtapd stays up (read/write/list still work); only bash
	// calls fail-closed while netns is unavailable.
	if !agent.NetnsAvailable() {
		log.Warn().
			Str("hint", "grant airtapd CAP_SYS_ADMIN (root) or enable unprivileged userns (/proc/sys/user/max_user_namespaces>0)").
			Msg("airtapd: netns isolation unavailable — bash tool will fail-closed until the box can create a CLONE_NEWNET netns")
	} else {
		log.Info().Msg("airtapd: netns isolation available — bash subprocesses will be loopback-only")
	}

	ln, err := tunnel.Listen(m.Box.Addr, m.Box.CA, *certPath, *keyPath)
	if err != nil {
		fail(err)
	}
	defer ln.Close()
	log.Info().Str("addr", m.Box.Addr).Msg("airtapd: listening (mTLS)")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("airtapd: accept")
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			handleConn(ctx, loop, c)
		}(conn)
	}
}

// handleConn reads the prompt (first line), points the loop's progress
// stream at the connection, and runs the ReAct loop. Tool calls and the
// final answer are written back over the conn as newline-terminated lines,
// which the thin client's tui.Stream renders.
func handleConn(ctx context.Context, loop *agent.Loop, conn net.Conn) {
	reader := bufio.NewReader(conn)
	prompt, err := reader.ReadString('\n')
	if err != nil {
		log.Error().Err(err).Msg("airtapd: read prompt")
		return
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Fprintln(conn, "airtap: empty prompt")
		log.Warn().Msg("airtapd: empty prompt")
		return
	}

	// Serialize so the shared loop's output sink is not clobbered by a
	// second concurrent connection mid-run.
	loopMu.Lock()
	defer loopMu.Unlock()

	// Pipe loop progress over this connection to the thin-client TUI.
	loop.SetOutput(conn)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := loop.Run(runCtx, prompt); err != nil {
		fmt.Fprintf(conn, "airtap: error: %v\n", err)
		log.Error().Err(err).Msg("airtapd: loop")
	}
}

// fail logs a fatal error and exits. Used for startup-time failures where
// continuing would leave the daemon in a broken state.
func fail(err error) {
	log.Fatal().Err(err).Msg("airtapd")
}
