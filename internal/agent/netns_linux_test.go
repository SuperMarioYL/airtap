//go:build linux

package agent

import (
	"net"
	"os/exec"
	"testing"
	"time"
)

// fix-bash-tool-egress-bypass regression: a subprocess isolated in a fresh
// CLONE_NEWNET netns must NOT be able to reach a TCP listener in the parent
// network namespace — this is the moat (raw-socket tools cannot dial off the
// box). The spec calls for a "non-loopback address"; here we approximate by
// binding the parent listener on 127.0.0.1 (loopback) and asserting the child
// still cannot reach it: a fresh CLONE_NEWNET netns has its OWN disjoint
// loopback (present but DOWN), so the child's dial to 127.0.0.1:port hits the
// child's empty loopback, never the parent's listener. This demonstrates the
// same property (isolated subprocess cannot reach a reachable-from-parent
// target) without depending on a configured LAN address.
//
// Environment-gated: requires (a) this process can create a CLONE_NEWNET netns
// (CAP_SYS_ADMIN or unprivileged userns) and (b) bash with /dev/tcp support.
// On CI/dev hosts without netns (the common case, incl. the macOS dev box) the
// test skips — the always-runnable fail-closed guard is in tools_test.go.
func TestNetnsIsolatesSubprocess(t *testing.T) {
	if !netnsAvailable() {
		t.Skip("netns unavailable on this host — skipping isolation regression (fail-closed covered by tools_test.go)")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH — skipping /dev/tcp isolation regression")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	accepted := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
		accepted <- struct{}{}
	}()

	// Isolated subprocess tries to reach the parent's listener via /dev/tcp.
	// On success (moat broken) it prints REACHED; on the expected failure it
	// prints BLOCKED and exits non-zero (caught by bash's `||`).
	cmd := exec.Command("bash", "-c",
		"timeout 3 bash -c 'echo > /dev/tcp/"+addr+"' && echo REACHED || echo BLOCKED")
	applyNetns(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A non-zero exit is acceptable here — it means the dial failed (good).
		// Only REACHED in the output would indicate a broken moat.
	}
	t.Logf("isolated bash output: %q err=%v", string(out), err)

	select {
	case <-accepted:
		t.Fatalf("MOAT BROKEN: isolated bash subprocess reached the parent listener at %s (output=%q)", addr, string(out))
	case <-time.After(500 * time.Millisecond):
		// Good — no connection was accepted. The child either got connection
		// refused (its own empty loopback) or timed out; either way it never
		// reached the parent's listener.
	}

	if contains(string(out), "REACHED") {
		t.Fatalf("isolated subprocess reported REACHED — moat broken (output=%q)", string(out))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
