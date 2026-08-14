package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuperMarioYL/airtap/internal/manifest"
)

// fix-manifest-tools-not-honored: filterTools must return only the requested
// subset of DefaultTools, in canonical order.
func TestFilterToolsSubset(t *testing.T) {
	got := filterTools(DefaultTools, []string{"read"})
	if len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("expected [read], got %+v", got)
	}
}

func TestFilterToolsOrderFollowsDefault(t *testing.T) {
	// request out of order; result preserves DefaultTools order
	got := filterTools(DefaultTools, []string{"bash", "read"})
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "bash" {
		t.Fatalf("expected [read bash] in canonical order, got %+v", names(got))
	}
}

func TestFilterToolsUnknownDropped(t *testing.T) {
	// manifest.Validate rejects unknown names upstream, but filterTools must
	// still drop any unknown (defensive) rather than panic.
	got := filterTools(DefaultTools, []string{"read", "nope"})
	if len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("expected only [read] (unknown dropped), got %+v", names(got))
	}
}

func TestFilterToolsEmpty(t *testing.T) {
	if got := filterTools(DefaultTools, nil); got != nil {
		t.Fatalf("expected nil for empty names, got %+v", got)
	}
}

// fix-bash-tool-egress-bypass: when netns isolation is UNAVAILABLE the bash
// tool MUST fail-closed — refuse to run and return an error — rather than
// silently fall back to an un-isolated exec that would leave the moat broken.
// This is the always-runnable guard test (the isolation regression test, which
// needs a real CLONE_NEWNET-capable host, lives in netns_linux_test.go).
func TestBashToolFailClosedWhenNetnsUnavailable(t *testing.T) {
	saved := netnsAvailable
	t.Cleanup(func() { netnsAvailable = saved })
	netnsAvailable = func() bool { return false }

	out, err := bashTool(context.Background(), "echo hi")
	if err == nil {
		t.Fatalf("expected fail-closed error when netns unavailable; got output %q", out)
	}
	if !strings.Contains(err.Error(), "netns") {
		t.Fatalf("error should mention netns isolation, got: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty output on fail-closed, got %q", out)
	}
}

// fix-manifest-tools-not-honored: NewLoop must advertise + dispatch ONLY the
// manifest's agent.tools set. A tools:[read] manifest yields a loop whose
// Dispatch rejects write/list/bash as "unknown or disabled".
func TestNewLoopFiltersToolsByManifest(t *testing.T) {
	m := &manifest.Manifest{
		Agent: manifest.AgentCfg{Workdir: ".", Tools: []string{"read"}},
	}
	l := NewLoop(m, nil, nil, nil)

	got := l.Tools()
	if len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("expected loop to advertise only [read], got %+v", names(got))
	}

	// Dispatch of a non-advertised tool must error.
	if _, err := l.Dispatch(context.Background(), "write", `{"path":"/tmp/x","content":"y"}`); err == nil {
		t.Fatalf("expected Dispatch(write) to be rejected under a tools:[read] manifest")
	}
	if _, err := l.Dispatch(context.Background(), "bash", `{"command":"echo hi"}`); err == nil {
		t.Fatalf("expected Dispatch(bash) to be rejected under a tools:[read] manifest")
	}

	// Dispatch of the advertised tool must work (read on a real, workdir-safe file).
	out, err := l.Dispatch(context.Background(), "read", "tools.go")
	if err != nil {
		t.Fatalf("expected Dispatch(read tools.go) to succeed, got: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty tools.go read, got empty")
	}
}

// Full tool set manifest advertises all four (regression vs the old hardcoded
// DefaultTools behavior).
func TestNewLoopAdvertisesAllToolsWhenUnrestricted(t *testing.T) {
	m := &manifest.Manifest{
		Agent: manifest.AgentCfg{Workdir: ".", Tools: []string{"read", "write", "list", "bash"}},
	}
	l := NewLoop(m, nil, nil, nil)
	got := names(l.Tools())
	want := []string{"read", "write", "list", "bash"}
	if len(got) != 4 {
		t.Fatalf("expected 4 tools, got %d (%v)", len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("tool %d: want %q got %q", i, w, got[i])
		}
	}
}

func names(ts []Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

// fix-file-tools-path-injection: safePath must reject absolute paths and
// traversal paths that escape the process workdir, so a model or
// prompt-injection cannot read /etc/shadow or write /etc/cron.d/evil.
func TestSafePathRejectsEscape(t *testing.T) {
	cases := []string{"/etc/shadow", "/etc/cron.d/evil", "../../etc/passwd", "/dev/null"}
	for _, p := range cases {
		if _, err := safePath(p); err == nil {
			t.Fatalf("safePath(%q) should reject (escapes workdir), got nil", p)
		}
	}
}

// safePath must accept a relative path that stays under the workdir.
func TestSafePathAcceptsWorkdirRelative(t *testing.T) {
	safe, err := safePath("tools.go")
	if err != nil {
		t.Fatalf("safePath(tools.go) should accept, got: %v", err)
	}
	if safe == "" {
		t.Fatal("safePath(tools.go) returned empty path")
	}
}
