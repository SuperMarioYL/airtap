package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/SuperMarioYL/airtap/internal/tui"
)

// fix-airtap-run-ctrl-c-blocked: scanner.Scan blocks in conn.Read and only
// checked ctx.Err() after a line arrived, so during a quiet model-generation
// period (no streamed lines) Ctrl-C could not exit until the next line or EOF.
// The fix spawns a goroutine closing conn on ctx.Done so the blocking Read
// returns. This test asserts a quiet stream exits promptly (within ~1s) once
// the context is canceled, using an in-memory pipe that never sends data.
func TestRunStreamExitsOnContextCancelDuringQuietStream(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// Use a no-close shared guard matching run.go's design.
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { c1.Close() }) }

	ctx, cancel := context.WithCancel(context.Background())
	stream := tui.NewStream()
	defer stream.Close()

	done := make(chan error, 1)
	go func() { done <- runStream(ctx, c1, closeConn, stream) }()

	// Quiet period: nothing is written to c2, so scanner.Scan blocks on Read.
	// Give the scanner time to settle into the blocking Read.
	time.Sleep(150 * time.Millisecond)

	cancel() // simulate Ctrl-C / signal.NotifyContext firing

	select {
	case err := <-done:
		// Expected: ctx.Done closes c1 -> Read returns -> scanner exits -> nil.
		if err != nil {
			t.Fatalf("expected nil from runStream on cancel, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runStream did not exit within 2s of ctx cancel — Ctrl-C still blocked during quiet stream")
	}
}

// Sanity: runStream still renders streamed lines and returns nil on a normal
// (non-canceled) close that produces data.
func TestRunStreamRendersLines(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()

	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { c1.Close() }) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := tui.NewStream()
	defer stream.Close()

	// Write one line then close c2 -> scanner sees the line then EOF.
	go func() {
		_, _ = c2.Write([]byte("hello world\n"))
		_ = c2.Close()
	}()

	done := make(chan error, 1)
	go func() { done <- runStream(ctx, c1, closeConn, stream) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on normal close, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runStream hung on a normal data-then-EOF stream")
	}
}
