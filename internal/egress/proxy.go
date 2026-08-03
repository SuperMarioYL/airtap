// Package egress implements the allowlist egress proxy that is the sole network
// egress point of the on-box agent process.
//
// Per the Airtap MVP plan §2, the EgressManifest binds an allowlist of dial
// targets (the on-box model endpoint, typically 127.0.0.1:8000) and an airgap
// mode. The daemon refuses any outbound dial not in egress.allow, and every
// attempt (allowed or denied) is appended to audit.log. A cloud runner cannot
// honor an airgap egress mode without abandoning its cloud backend, so this proxy
// is the structural moat the manifest enforces by construction.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/SuperMarioYL/airtap/internal/audit"
)

// ErrDenied is returned by Dial when the target address is not on the allow list.
var ErrDenied = errors.New("airtap: egress denied by allowlist")

// auditRecorder is the subset of *audit.Audit the proxy needs (Record only).
// Defined as an interface so the fail-closed audit path is testable with a fake
// recorder (fix-egress-audit-write-swallowed). *audit.Audit satisfies it.
type auditRecorder interface {
	Record(target string, allowed bool) error
}

// Proxy is an allowlist egress proxy. It is installed as the process-wide HTTP
// dialer by the agent loop (agent.installEgress) so that every outbound dial —
// notably the model client's HTTP calls — is intercepted here.
type Proxy struct {
	allow map[string]struct{}
	audit auditRecorder
}

// NewProxy builds a Proxy that permits dials to any address in allow and denies
// (and audit-logs) everything else. Each allow entry is a "host:port" string as
// it appears in airtap.yaml egress.allow.
func NewProxy(allow []string, a *audit.Audit) *Proxy {
	p := &Proxy{
		allow: make(map[string]struct{}, len(allow)),
	}
	// Store the recorder only when non-nil: a nil *audit.Audit assigned to an
	// interface would be a non-nil typed-nil interface, which would defeat the
	// `p.audit != nil` guard below and panic on Record.
	if a != nil {
		p.audit = a
	}
	for _, addr := range allow {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		p.allow[normalizeAddr(addr)] = struct{}{}
	}
	return p
}

// Allowed reports whether addr is on the allow list.
func (p *Proxy) Allowed(addr string) bool {
	_, ok := p.allow[normalizeAddr(addr)]
	return ok
}

// Dial is the single network egress point (no context). It delegates to
// DialContext with a background context so legacy callers keep the same
// allow/audit/fail-closed semantics. addr is a "host:port" string as provided
// by net/http's transport DialContext.
//
// On allow: audit-logs allowed=true, dials the real network, returns the conn.
// On deny:  audit-logs allowed=false, returns ErrDenied without dialing.
//
// Both verdicts are recorded so audit.log shows every attempt, per §2. The
// allow path is fail-closed on audit-write errors (fix-egress-audit-write-
// swallowed): no egress without a durable audit record.
func (p *Proxy) Dial(network, addr string) (net.Conn, error) {
	return p.DialContext(context.Background(), network, addr)
}

// DialContext is the context-aware single network egress point. It is wired
// into http.DefaultTransport.DialContext by agent.installEgress so SIGTERM to
// airtapd and the model client's HTTPTimeout propagate into an in-flight dial
// (fix-egress-dial-ignores-context): the previous net.Dial ignored ctx and a
// dial to an allowed-but-unreachable target blocked ~1-2 min of SYN retries,
// hanging client.Chat on a downed endpoint.
//
// Denied targets return ErrDenied immediately, before any dial. The allow
// path fails closed if the audit record cannot be made durable.
func (p *Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !p.Allowed(addr) {
		// Deny path: best-effort audit. The verdict (ErrDenied) holds
		// regardless of whether the audit write succeeds — a failed audit
		// write on a deny is logged to stderr but must not downgrade a
		// deny into an allow.
		if p.audit != nil {
			if err := p.audit.Record(addr, false); err != nil {
				fmt.Fprintf(os.Stderr, "airtap: egress deny audit write failed (target=%s): %v\n", addr, err)
			}
		}
		return nil, ErrDenied
	}
	// Allow path: fail-closed on audit integrity. If the tamper-evident
	// record cannot be made durable (disk full / broken file), the dial is
	// DENIED — never silently allowed — so the operator's belief that "no
	// egress occurred" matches reality (fix-egress-audit-write-swallowed).
	if p.audit != nil {
		if err := p.audit.Record(addr, true); err != nil {
			return nil, fmt.Errorf("airtap: egress denied (audit record failed for %s): %w", addr, err)
		}
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// normalizeAddr canonicalizes a "host:port" address so that allowlist lookups are
// case-insensitive on the host and tolerant of surrounding whitespace / IPv6
// brackets. Bare hosts (no port) are lower-cased and returned as-is.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "[")
	addr = strings.TrimSuffix(addr, "]")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.ToLower(addr)
	}
	return net.JoinHostPort(strings.ToLower(host), port)
}
