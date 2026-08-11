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
