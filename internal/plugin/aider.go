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
	"os"
	"os/exec"
)

// AiderPlugin drives the Aider coding agent behind the host's moat. It spawns
// `aider` as a subprocess isolated in a CLONE_NEWNET netns (loopback-only) so
// the agent's model + subprocess traffic cannot dial off the box — the same
// isolation the built-in bash tool uses. ctx cancellation kills the subprocess
// (fix-bash-tool-uncancelable-hang applied the same exec.CommandContext pattern
// to the agent loop's tools).
//
// fix-aider-plugin-ignores-onbox-endpoint (v0.5.0): the plugin carries the
// manifest's on-box model endpoint + name so aider targets 127.0.0.1 behind the
// host egress proxy + netns moat instead of its default cloud endpoint (which
// the moat blocks by design on a 数据不出境 box). v0.4 spawned aider with no
// --model and no endpoint env, so the headline plugin could not complete a
// single task on the exact surface the product targets.
type AiderPlugin struct {
	modelEndpoint string // manifest model.endpoint, e.g. http://127.0.0.1:8000/v1
	modelName     string // manifest model.name, e.g. deepseek-v3
}

// NewAiderPlugin returns a ready Aider adapter bound to the on-box model
// endpoint and name.
func NewAiderPlugin(modelEndpoint, modelName string) *AiderPlugin {
	return &AiderPlugin{modelEndpoint: modelEndpoint, modelName: modelName}
}

// Name is the plugin identifier the manifest resolves via agent.plugin.
func (a *AiderPlugin) Name() string { return "aider" }

// Tools returns the tool surface this plugin advertises to the model. Aider
// manages its own internal tool surface (file edits, repo ops); the host still
// filters by manifest.agent.tools, so returning nil means "no host-side tools"
// — aider drives its own workflow.
func (a *AiderPlugin) Tools() []Tool { return nil }

// Run spawns `aider --message <prompt>` as a netns-isolated subprocess pointed
// at the on-box model endpoint. The host's egress proxy (installed as
// http.DefaultTransport.DialContext) still gates any Go-process HTTP; the netns
// closes the raw-socket gap for aider's own subprocess dials. ctx cancellation
// sends SIGKILL to the subprocess.
func (a *AiderPlugin) Run(ctx context.Context, prompt string, tools []Tool) error {
	if _, err := exec.LookPath("aider"); err != nil {
		return fmt.Errorf("aider: binary not found on the box (install aider: pip install aider-chat); %w", err)
	}
	cmd := buildAiderCmd(ctx, prompt, a.modelEndpoint, a.modelName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aider: %w: %s", err, string(out))
	}
	return nil
}

// buildAiderCmd constructs the netns-isolated aider subprocess for a prompt,
// bound to the on-box model endpoint + name. Factored out of Run so the
// model-wiring regression can assert the cmd targets the on-box endpoint +
// brings the netns loopback up without the aider binary installed
// (fix-aider-plugin-ignores-onbox-endpoint, fix-aider-netns-loopback-down-
// blocks-onbox-model).
//
// Aider defaults to OpenAI's cloud endpoint; on a 数据不出境 box that dial is
// exactly what the CLONE_NEWNET netns blocks, so we MUST point aider at the
// on-box endpoint explicitly: OPENAI_API_BASE=<endpoint> (the OpenAI-compatible
// base URL the vLLM-Ascend / MindIE shim serves) + a non-empty OPENAI_API_KEY
// (aider refuses to run without one; the on-box shim ignores the value) + the
// --model <name> flag (fix-aider-plugin-ignores-onbox-endpoint, v0.5.0).
//
// fix-aider-netns-loopback-down-blocks-onbox-model (v0.6.0): the aider
// subprocess runs in a fresh CLONE_NEWNET netns (applyNetnsToCmd below) whose
// loopback is DOWN by kernel default, so aider's model dial to the on-box
// 127.0.0.1:8000 endpoint (set via OPENAI_API_BASE in v0.5.0) failed with
// ENETUNREACH — the v0.5.0 endpoint fix never actually worked inside the moat.
// The script brings lo UP before exec-ing aider so the isolated subprocess can
// reach the on-box model (loopback) while retaining no default route and no
// non-loopback interface — the plan's intended loopback-only netns. The child
// holds CAP_NET_ADMIN in the new netns (airtapd as root CAP_SYS_ADMIN, or
// unprivileged userns granting a full cap set in the new ns), so
// `ip link set lo up` succeeds on the 信创 surface (iproute2 on Kylin/UOS/
// openEuler). The bash tool's netns is untouched and stays fully-disconnected.
//
// The prompt travels via the AIRTAP_PROMPT env var, NOT inline in the script,
// so user-controlled prompt text is never shell-interpreted: the double-quoted
// env expansion below is data, not re-evaluated as syntax (no prompt injection).
func buildAiderCmd(ctx context.Context, prompt, modelEndpoint, modelName string) *exec.Cmd {
	const script = `ip link set lo up 2>/dev/null || true
exec aider --message "$AIRTAP_PROMPT" --no-auto-commits --yes-always --model "$AIRTAP_MODEL"`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	// Inherit the parent env (PATH, etc.) and overlay the on-box endpoint +
	// the prompt/model values the script expands (never inlined).
	cmd.Env = append(os.Environ(),
		"AIRTAP_PROMPT="+prompt,
		"AIRTAP_MODEL="+modelName,
		"OPENAI_API_BASE="+modelEndpoint,
		"OPENAI_API_KEY=airtap-onbox",
	)
	applyNetnsToCmd(cmd) // build-tag-separated; CLONE_NEWNET on Linux, no-op elsewhere
	return cmd
}

// AiderLoader resolves the "aider" plugin name to an AiderPlugin instance. The
// host registers it at startup with the manifest's model endpoint + name so the
// adapter can wire aider to the on-box model:
// `registry.Register("aider", &plugin.AiderLoader{ModelEndpoint: m.Model.Endpoint, ModelName: m.Model.Name})`.
type AiderLoader struct {
	ModelEndpoint string // manifest model.endpoint
	ModelName     string // manifest model.name
}

// Load returns an AiderPlugin bound to the loader's on-box model endpoint when
// name == "aider"; otherwise ErrUnknownPlugin.
func (l *AiderLoader) Load(name string) (AgentPlugin, error) {
	if name == "aider" {
		return NewAiderPlugin(l.ModelEndpoint, l.ModelName), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, name)
}
