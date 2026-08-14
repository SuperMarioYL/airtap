// Package plugin — aider.go is the concrete Aider runtime wrapper
// (feat-agent-plugin-runtime-wrappers, v0.4.0). v0.3.0 shipped the
// AgentPlugin spec/interface + a Registry that resolved every name to
// ErrUnknownPlugin; this is the first concrete loader. The host (airtapd)
// resolves it via the Registry; the host owns the egress/audit moat, the
// plugin owns the agent's workflow and never dials out directly.
package plugin

import (
	"context"
	"fmt"
	"os/exec"
)

// AiderPlugin drives the Aider coding agent behind the host's moat. It spawns
// `aider` as a subprocess isolated in a CLONE_NEWNET netns (loopback-only) so
// the agent's model + subprocess traffic cannot dial off the box — the same
// isolation the built-in bash tool uses. ctx cancellation kills the subprocess
// (fix-bash-tool-uncancelable-hang applied the same exec.CommandContext pattern
// to the agent loop's tools).
type AiderPlugin struct{}

// NewAiderPlugin returns a ready Aider adapter.
func NewAiderPlugin() *AiderPlugin { return &AiderPlugin{} }

// Name is the plugin identifier the manifest resolves via agent.plugin.
func (a *AiderPlugin) Name() string { return "aider" }

// Tools returns the tool surface this plugin advertises to the model. Aider
// manages its own internal tool surface (file edits, repo ops); the host still
// filters by manifest.agent.tools, so returning nil means "no host-side tools"
// — aider drives its own workflow.
func (a *AiderPlugin) Tools() []Tool { return nil }

// Run spawns `aider --message <prompt>` as a netns-isolated subprocess. The
// host's egress proxy (installed as http.DefaultTransport.DialContext) still
// gates any Go-process HTTP; the netns closes the raw-socket gap for aider's
// own subprocess dials. ctx cancellation sends SIGKILL to the subprocess.
func (a *AiderPlugin) Run(ctx context.Context, prompt string, tools []Tool) error {
	if _, err := exec.LookPath("aider"); err != nil {
		return fmt.Errorf("aider: binary not found on the box (install aider: pip install aider-chat); %w", err)
	}
	cmd := exec.CommandContext(ctx, "aider",
		"--message", prompt,
		"--no-auto-commits",
		"--yes-always",
	)
	applyNetnsToCmd(cmd) // build-tag-separated; CLONE_NEWNET on Linux, no-op elsewhere
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aider: %w: %s", err, string(out))
	}
	return nil
}

// AiderLoader resolves the "aider" plugin name to an AiderPlugin instance. The
// host registers it at startup: `registry.Register("aider", &plugin.AiderLoader{})`.
type AiderLoader struct{}

// Load returns an AiderPlugin when name == "aider"; otherwise ErrUnknownPlugin.
func (l *AiderLoader) Load(name string) (AgentPlugin, error) {
	if name == "aider" {
		return NewAiderPlugin(), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, name)
}
