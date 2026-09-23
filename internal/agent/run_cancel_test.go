package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuperMarioYL/airtap/internal/egress"
	"github.com/SuperMarioYL/airtap/internal/manifest"
	"github.com/SuperMarioYL/airtap/internal/model"
)

// TestRunCancelsInFlightModelCall pins the disconnect contract end to end:
// canceling the run context must interrupt an IN-FLIGHT model call, not just
// take effect between turns.
//
// Red on v0.6.0: model.Client.Chat builds its request with http.NewRequest
// (no context), so Loop.Run stays blocked inside Chat until the endpoint
// answers — a thin-client disconnect (connContext cancel in airtapd) or
// SIGTERM leaves the on-box GPU generating for up to model.HTTPTimeout (5m),
// and because handleConn serializes runs on loopMu, the NEXT `airtap run`
// queues behind the orphaned call with no output.
//
// After the fix (Chat(ctx, ...) + http.NewRequestWithContext) Run returns
// context.Canceled promptly after cancel.
func TestRunCancelsInFlightModelCall(t *testing.T) {
	const serverDelay = 2 * time.Second
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "final"}}},
		})
	}))
	defer srv.Close()

	// Route the client's dial through the real egress proxy (the process-wide
	// path) with only the test endpoint allowed; restore the default transport
	// so the process-wide installEgress does not leak into other tests.
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skipf("default transport is %T, not *http.Transport", http.DefaultTransport)
	}
	origDial := tr.DialContext
	t.Cleanup(func() { tr.DialContext = origDial })

	addr := strings.TrimPrefix(srv.URL, "http://")
	m := &manifest.Manifest{
		Agent: manifest.AgentCfg{Workdir: ".", Tools: []string{"read"}},
	}
	proxy := egress.NewProxy([]string{addr}, nil)
	client := model.NewClient(srv.URL, "test-model")
	l := NewLoop(m, client, proxy, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, "do it") }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if elapsed >= serverDelay {
			t.Fatalf("Run returned %v after %v — cancellation did not interrupt the in-flight model call (the request was built without the run context)", err, elapsed)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(serverDelay + 2*time.Second):
		t.Fatal("Run did not return after cancel — blocked in the in-flight model call")
	}
}
