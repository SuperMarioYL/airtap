package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// fix-safepath-symlink-cwd-breaks-file-tools (v0.5.0): safePath resolved the
// target through filepath.EvalSymlinks but left cwd (os.Getwd) UNRESOLVED, so a
// workdir reached via a symlink (macOS /tmp -> /private/tmp, or a bind-mounted
// / shortcut-linked repo root on a 信创 box) was falsely rejected as an escape
// — read/write/list refused every file and `airtap run` was dead on arrival.
// This regression forces the scenario on ANY host (incl. Linux CI where /tmp is
// not a symlink) by chdir-ing through a synthetic symlink to the real workdir.
func TestSafePathAcceptsSymlinkedWorkdir(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "file.go"), []byte("package p"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create symlink (sandbox disallows?): %v", err)
	}

	// Restore cwd on exit so neighboring tests keep their working directory.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	if err := os.Chdir(linkDir); err != nil {
		t.Fatalf("chdir into symlinked workdir: %v", err)
	}

	safe, err := safePath("file.go")
	if err != nil {
		t.Fatalf("safePath(file.go) under a symlinked workdir should accept, got: %v", err)
	}
	if safe == "" {
		t.Fatal("safePath returned empty path")
	}
}
