package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool is an executable agent tool. Exec receives the run context (so a hanging
// command can be canceled — fix-bash-tool-uncancelable-hang) and the raw arguments
// string (as produced by the model — typically a JSON object per the tool's schema)
// and returns the tool's text output.
type Tool struct {
	Name string
	Exec func(ctx context.Context, args string) (string, error)
}

// The four built-in tools available on every box: read, write, list, bash.
// They operate on paths relative to the process working directory; the daemon
// is expected to chdir into manifest.agent.workdir before running the loop.
var (
	read  = Tool{Name: "read", Exec: readTool}
	write = Tool{Name: "write", Exec: writeTool}
	list  = Tool{Name: "list", Exec: listTool}
	bash  = Tool{Name: "bash", Exec: bashTool}
)

// DefaultTools is the set of built-in tools the loop advertises to the model.
var DefaultTools = []Tool{read, write, list, bash}

// filterTools returns the subset of all whose Name appears in names, preserving
// the canonical DefaultTools order. MVP plan §5 fix-manifest-tools-not-honored:
// an operator restricting `tools: [read]` must get ONLY read advertised and
// dispatched — the hardcoded DefaultTools set violated the EgressManifest
// contract. Unknown names are rejected earlier in manifest.Validate, so by the
// time the loop runs every entry in names is a known tool.
func filterTools(all []Tool, names []string) []Tool {
	if len(names) == 0 {
		return nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := make([]Tool, 0, len(names))
	for _, t := range all {
		if want[t.Name] {
			out = append(out, t)
			delete(want, t.Name) // first match wins; avoids dup-driven entries
		}
	}
	return out
}

// Dispatch looks up a tool by name in DefaultTools and invokes it with args.
// Unknown tool names return an error so the loop can surface the failure back to
// the model as a tool-result.
func Dispatch(ctx context.Context, name, args string) (string, error) {
	for _, t := range DefaultTools {
		if t.Name == name {
			return t.Exec(ctx, args)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// --- tool implementations -----------------------------------------------------

// safePath resolves p against the process working directory (set by airtapd to
// manifest.agent.workdir) and rejects any path that escapes the workdir root
// after symlink resolution (fix-file-tools-path-injection). The file tools
// (read/write/list) were left without a filesystem jail while the bash tool was
// netns-hardened for network egress; safePath closes that gap so a model or
// prompt-injection cannot read /etc/shadow or write /etc/cron.d/evil.
func safePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("path: resolve %s: %w", p, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("path: workdir: %w", err)
	}
	// Evaluate symlinks so ../ and symlink chains can't escape.
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// A non-existent target is fine for write (MkdirAll creates it); resolve
		// the parent and check that stays under the workdir.
		eval = abs
	}
	if eval == "" {
		eval = abs
	}
	// Walk up to the first existing ancestor for symlink eval, then re-append.
	if _, statErr := os.Stat(eval); statErr != nil {
		parent := filepath.Dir(eval)
		for parent != "/" {
			if _, e := os.Stat(parent); e == nil {
				if rp, e := filepath.EvalSymlinks(parent); e == nil {
					eval = filepath.Join(rp, filepath.Base(abs))
				}
				break
			}
			parent = filepath.Dir(parent)
		}
	}
	rel, err := filepath.Rel(cwd, eval)
	if err != nil {
		return "", fmt.Errorf("path: %s escapes workdir %s", p, cwd)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path: %s escapes workdir %s (resolved %s)", p, cwd, eval)
	}
	return abs, nil
}

// readTool returns the contents of a file. args is the path (plain string), or a
// JSON {"path":"..."} object.
func readTool(ctx context.Context, args string) (string, error) {
	path := firstArg(args, "path")
	if path == "" {
		return "", fmt.Errorf("read: empty path")
	}
	safe, err := safePath(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(safe)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

// writeTool writes content to a file, creating parent directories as needed. args
// must be JSON {"path":"...","content":"..."}.
func writeTool(ctx context.Context, args string) (string, error) {
	var v struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return "", fmt.Errorf("write: parse args: %w", err)
	}
	if v.Path == "" {
		return "", fmt.Errorf("write: empty path")
	}
	safe, err := safePath(v.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(safe), 0o755); err != nil {
		return "", fmt.Errorf("write %s: mkdir: %w", v.Path, err)
	}
	if err := os.WriteFile(safe, []byte(v.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", v.Path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(v.Content), v.Path), nil
}

// listTool lists the entries of a directory. args is the dir path (plain string,
// defaults to "."), or JSON {"path":"..."}.
func listTool(ctx context.Context, args string) (string, error) {
	dir := firstArg(args, "path")
	if dir == "" {
		dir = "."
	}
	safe, err := safePath(dir)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(safe)
	if err != nil {
		return "", fmt.Errorf("list %s: %w", dir, err)
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&b, name)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// bashTool runs a shell command via `sh -c` and returns combined stdout/stderr.
// args is the command (plain string) or JSON {"command":"..."}.
//
// MOAT-CRITICAL (MVP plan §5 fix-bash-tool-egress-bypass): the subprocess is
// isolated in a fresh CLONE_NEWNET netns (loopback-only, no default route) so
// raw-socket tools (curl/wget/nc/bespoke binaries) cannot dial off the box,
// bypassing the Go-process egress gate. If netns isolation is unavailable
// (hardened kernel, no CAP_SYS_ADMIN, unprivileged userns disabled) the tool
// FAILS CLOSED — it refuses to run and returns an error to the agent loop
// rather than silently falling back to an un-isolated exec. The proxy-env
// fallback is intentionally dropped (no listening CONNECT proxy; raw-socket
// tools ignore env). See netns_{linux,other}.go.
func bashTool(ctx context.Context, args string) (string, error) {
	cmd := firstArg(args, "command")
	if cmd == "" {
		return "", fmt.Errorf("bash: empty command")
	}
	if !netnsAvailable() {
		return "", fmt.Errorf("bash: netns isolation unavailable; refusing to run (grant airtapd CAP_SYS_ADMIN or enable unprivileged userns to preserve 数据不出境)")
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	applyNetns(c)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("bash: %w", err)
	}
	return string(out), nil
}

// NetnsAvailable reports whether the bash tool can isolate subprocesses in a
// CLONE_NEWNET netns on this host. airtapd probes it at startup to surface the
// CAP_SYS_ADMIN / unprivileged-userns requirement before the first `airtap run`.
func NetnsAvailable() bool { return netnsAvailable() }

// --- tool schema metadata (sent to the model) --------------------------------

// toolDescription returns the human-readable description advertised to the model.
func toolDescription(name string) string {
	switch name {
	case "read":
		return "Read a file from the box filesystem and return its contents."
	case "write":
		return "Write content to a file on the box filesystem, creating parent directories as needed."
	case "list":
		return "List the entries of a directory on the box filesystem."
	case "bash":
		return "Run a shell command on the box and return combined stdout/stderr."
	}
	return ""
}

// toolSchema returns the OpenAI-style JSON-schema parameters object for a tool.
func toolSchema(name string) map[string]any {
	switch name {
	case "read", "list":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "filesystem path"},
			},
			"required": []string{"path"},
		}
	case "write":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "filesystem path"},
				"content": map[string]any{"type": "string", "description": "file content"},
			},
			"required": []string{"path", "content"},
		}
	case "bash":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "shell command"},
			},
			"required": []string{"command"},
		}
	}
	return nil
}

// firstArg extracts a single argument from the model's args string. It accepts
// either a JSON object {"<key>":"..."} or a plain string (trimmed). This lets the
// model call single-arg tools either way without the loop caring.
func firstArg(args, key string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		var v map[string]string
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			return strings.TrimSpace(v[key])
		}
	}
	return s
}
