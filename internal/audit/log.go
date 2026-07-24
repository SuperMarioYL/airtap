// Package audit is the append-only egress trail. Every outbound dial
// attempt the daemon makes (allowed or denied) is recorded here, so
// `airtap audit` can render a tamper-evident-by-append log of exactly
// which targets the box talked to. Under a healthy airgap manifest the
// trail contains only the on-box model endpoint and nothing else.
package audit

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Audit is a concurrency-safe append-only log writer. It opens the
// underlying file in append+create mode and never seeks, so concurrent
// Record calls append one line each without corrupting prior entries.
type Audit struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// New opens (creating if needed) the audit log at path for append-only
// writes. The file is opened with 0600 so the trail is only readable by
// the box operator.
func New(path string) (*Audit, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Audit{path: path, f: f}, nil
}

// Record appends a single timestamped line describing an egress attempt.
// allowed=true means the dial was permitted by the egress policy;
// allowed=false means it was denied. The line is flushed before return so
// the trail is durable even if the daemon is killed mid-run.
func (a *Audit) Record(target string, allowed bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	verdict := "DENY"
	if allowed {
		verdict = "ALLOW"
	}
	line := fmt.Sprintf("%s target=%s verdict=%s\n", ts, target, verdict)
	if _, err := a.f.WriteString(line); err != nil {
		return fmt.Errorf("audit: write %s: %w", a.path, err)
	}
	if err := a.f.Sync(); err != nil {
		// fsync best-effort on the underlying file; do not mask the write.
		return fmt.Errorf("audit: sync %s: %w", a.path, err)
	}
	return nil
}

// Path returns the on-disk path of the audit log.
func (a *Audit) Path() string { return a.path }

// Close flushes and closes the audit log. After Close the Audit must not
// be used.
func (a *Audit) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	if err := a.f.Sync(); err != nil {
		a.f.Close()
		return fmt.Errorf("audit: close %s: %w", a.path, err)
	}
	err := a.f.Close()
	a.f = nil
	return err
}
