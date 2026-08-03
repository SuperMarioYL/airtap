package egress

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// failingRecorder is a test auditRecorder whose Record always errors, to
// simulate a broken audit sink (disk full / file gone) for the fail-closed
// path (fix-egress-audit-write-swallowed).
type failingRecorder struct{ calls int }

func (f *failingRecorder) Record(target string, allowed bool) error {
	f.calls++
	return fmt.Errorf("disk full")
}

// TestDialContextFailClosedOnAuditError: on an ALLOWED dial, if the audit
// record cannot be made durable, the dial MUST be denied (fail-closed) — never
// silently allowed. This is the moat's tamper-evident-by-append contract.
func TestDialContextFailClosedOnAuditError(t *testing.T) {
	// "allowed" target on a high port that nothing listens on; the dial must
	// never reach the network because the audit write fails first.
	p := NewProxy([]string{"127.0.0.1:65500"}, nil)
	rec := &failingRecorder{}
	p.audit = rec // in-package seam; simulate a broken audit sink

	conn, err := p.DialContext(context.Background(), "tcp", "127.0.0.1:65500")
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("expected dial to be fail-closed when audit write errors; got conn")
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly one audit Record call on the allow path, got %d", rec.calls)
	}
	if !errors.Is(err, err) { // sanity: non-nil error
		t.Fatalf("expected non-nil error")
	}
}

// TestDialContextDenyReturnsImmediatelyWithoutDial: a denied target returns
// ErrDenied before any network dial, even with a background context.
func TestDialContextDenyReturnsImmediatelyWithoutDial(t *testing.T) {
	p := NewProxy([]string{"127.0.0.1:8000"}, nil) // only 8000 allowed
	_, err := p.DialContext(context.Background(), "tcp", "10.0.0.99:443")
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied for non-allowlisted target, got %v", err)
	}
}

// TestDialContextRespectsCanceledContext (fix-egress-dial-ignores-context):
// an allowed-but-unreachable target dial must abort promptly when the context
// is canceled — SIGTERM + the client timeout now propagate. Previously
// net.Dial ignored ctx and blocked ~1-2 min of SYN retries.
func TestDialContextRespectsCanceledContext(t *testing.T) {
	// Use a non-routable address that would hang on dial if ctx were ignored.
	// 192.0.2.1 is TEST-NET-1 (RFC 5737): no route, so a real dial would block
	// until SYN timeout. With a pre-canceled context, DialContext must return
	// near-instantly with ctx.Err.
	p := NewProxy([]string{"192.0.2.1:80"}, nil) // allowed by allowlist
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	start := time.Now()
	_, err := p.DialContext(ctx, "tcp", "192.0.2.1:80")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected error from dialing unreachable target with canceled ctx, got nil")
	}
	// Must return far faster than a SYN timeout (~1-2 min). 2s is generous
	// headroom for a context-canceled short-circuit.
	if elapsed > 2*time.Second {
		t.Fatalf("dial did not respect canceled context: took %v (expected <2s)", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		// net.Dialer returns ctx.Err() directly on a canceled context.
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// noopRecorder records calls without failing, for the deny-path best-effort
// coverage.
type noopRecorder struct{ allowed, denied int }

func (n *noopRecorder) Record(target string, allowed bool) error {
	if allowed {
		n.allowed++
	} else {
		n.denied++
	}
	return nil
}

func TestDialContextDenyPathLogsAuditBestEffort(t *testing.T) {
	rec := &noopRecorder{}
	p := NewProxy([]string{"127.0.0.1:8000"}, nil)
	p.audit = rec
	_, err := p.DialContext(context.Background(), "tcp", "10.0.0.99:443")
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if rec.denied != 1 {
		t.Fatalf("expected 1 deny audit record, got %d", rec.denied)
	}
}
