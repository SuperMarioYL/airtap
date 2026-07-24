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
	"errors"
	"net"
	"strings"

	"github.com/SuperMarioYL/airtap/internal/audit"
)

// ErrDenied is returned by Dial when the target address is not on the allow list.
var ErrDenied = errors.New("airtap: egress denied by allowlist")

// Proxy is an allowlist egress proxy. It is installed as the process-wide HTTP
// dialer by the agent loop (agent.installEgress) so that every outbound dial —
// notably the model client's HTTP calls — is intercepted here.
type Proxy struct {
	allow map[string]struct{}
	audit *audit.Audit
}

// NewProxy builds a Proxy that permits dials to any address in allow and denies
// (and audit-logs) everything else. Each allow entry is a "host:port" string as
// it appears in airtap.yaml egress.allow.
func NewProxy(allow []string, a *audit.Audit) *Proxy {
	p := &Proxy{
		allow: make(map[string]struct{}, len(allow)),
		audit: a,
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

// Dial is the single network egress point. addr is a "host:port" string as
// provided by net/http's transport DialContext.
//
// On allow: audit-logs allowed=true, dials the real network, returns the conn.
// On deny:  audit-logs allowed=false, returns ErrDenied without dialing.
//
// Both verdicts are recorded so audit.log shows every attempt, per §2.
func (p *Proxy) Dial(network, addr string) (net.Conn, error) {
	if p.Allowed(addr) {
		if p.audit != nil {
			_ = p.audit.Record(addr, true)
		}
		return net.Dial(network, addr)
	}
	if p.audit != nil {
		_ = p.audit.Record(addr, false)
	}
	return nil, ErrDenied
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
