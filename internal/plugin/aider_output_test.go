package plugin

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// outputSetter is the streaming seam the daemon needs: airtapd hands the
// plugin the connection writer so the external agent's progress streams to
// the thin client, exactly like agent.Loop.SetOutput does for the built-in
// loop.
type outputSetter interface{ SetOutput(io.Writer) }

// TestAiderRunStreamsOutputToHost pins the plugin-path streaming contract.
//
// Red on v0.6.0: AiderPlugin.Run captures the subprocess's CombinedOutput and
// DISCARDS it on success (aider.go: `out, err := cmd.CombinedOutput()`; the
// success return is plain nil), and handleConn writes nothing to the conn on
// success — so `airtap run` against `agent.plugin: aider` streams ZERO lines
// to the laptop: no progress, no edits summary, nothing. The run looks hung
// until it silently finishes empty. On failure the output only surfaces
// embedded in the error string.
func TestAiderRunStreamsOutputToHost(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "aider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'aider: added /health handler to server.go'\necho 'aider: committed Abracadabra'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := NewAiderPlugin("http://127.0.0.1:8000/v1", "deepseek-v3")
	var ap any = p // interface indirection so the assertion compiles against v0.6.0 too
	s, ok := ap.(outputSetter)
	if !ok {
		t.Fatalf("AiderPlugin does not implement SetOutput(io.Writer) — the daemon cannot hand the plugin the connection writer, so a plugin run streams NOTHING to the thin client on success (v0.6.0 discards CombinedOutput)")
	}
	var buf bytes.Buffer
	s.SetOutput(&buf)
	if err := p.Run(context.Background(), "add /health", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("aider: added /health handler to server.go")) {
		t.Fatalf("plugin output was not streamed to the host writer; got %q", buf.String())
	}
}
