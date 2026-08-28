package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// fix-daemon-loop-keeps-running-after-client-disconnect: runCtx must be tied
// to the conn lifetime so a thin-client disconnect (Ctrl-C on `airtap run`
// closes the mTLS conn) cancels the on-box loop. connContext drains the conn
// in a goroutine and cancels the returned context on the first read error/EOF.
// This test asserts a client-side close cancels the run context promptly —
// without it the loop would keep issuing model calls into a dead conn for up
// to MaxIterations (~40 min of wasted GPU time) and block reconnecting
// operators behind loopMu.
func TestConnContextCancelsOnClientDisconnect(t *testing.T) {
	c1, c2 := net.Pipe() // c1 = daemon side, c2 = client side
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := connContext(context.Background(), c1)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatalf("ctx canceled before the client disconnect (false positive)")
	default:
	}

	// Simulate `airtap run` Ctrl-C: the thin client closes its end of the mTLS
	// conn. The daemon-side drain goroutine's io.Copy returns (EOF / read
	// error) and cancels ctx.
	c2.Close()

	select {
	case <-ctx.Done():
		// expected: conn drain saw the close -> cancel
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx not canceled within 2s of client disconnect — the loop would keep running into a dead conn")
	}
}

// Negative case: an idle but OPEN connection must NOT cancel the context
// (no false-positive cancellation that would abort a healthy long run).
func TestConnContextStaysAliveWhileConnOpen(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := connContext(context.Background(), c1)
	defer cancel()

	// Conn stays open and idle; ctx must remain alive.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("ctx canceled while the conn is still open (false positive — a healthy idle run would be aborted)")
	default:
	}
}

// fix-airtapd-sigterm-blocked-in-accept (v0.5.0): SIGTERM must unblock the
// daemon's accept loop, not hang in ln.Accept until the next inbound connection.
// The prior for-loop checked ctx.Err() only AFTER Accept returned; on a quiet
// box `systemctl stop airtapd` (SIGTERM) hung the daemon because nothing closed
// the listener on ctx.Done. The fix's ctx.Done -> ln.Close goroutine (sync.Once
// guarded) closes the listener so Accept returns net.ErrClosed and the loop
// exits. This uses a plain tcp listener (the ctx->close behavior is
// transport-agnostic) and asserts the loop exits within ~2s of cancel.
func TestAcceptLoopExitsOnContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		acceptLoop(ctx, ln, func(net.Conn) {})
		close(done)
	}()

	// Quiet box: no inbound connections. Give the loop time to settle into the
	// blocking Accept, then simulate SIGTERM via ctx cancel.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// expected: ctx.Done closed ln -> Accept returned net.ErrClosed -> loop exited
	case <-time.After(2 * time.Second):
		t.Fatalf("acceptLoop did not exit within 2s of ctx cancel — SIGTERM still blocked in ln.Accept")
	}
}
