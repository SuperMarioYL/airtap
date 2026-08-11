// Package plugin defines the agent-plugin spec (amendment v0.3.0
// feat-agent-plugin-spec): the contract an external coding agent
// (Aider / Cline / Continue) implements so it can be driven behind Airtap's
// EgressManifest + audit + CLONE_NEWNET netns moat WITHOUT re-implementing
// the egress/audit isolation itself.
//
// v0.3.0 ships the spec/interface + a minimal registry/loader stub ONLY.
// Concrete runtime wrappers per external agent are explicitly out_of_scope
// (v0.4.0+): the host owns the egress moat + audit + manifest tool filter;
// the plugin owns the agent's workflow and never dials out directly.
package plugin

import (
	"context"
	"errors"
	"fmt"
)

// Tool is the plugin-facing tool descriptor: a name + an exec function. It
// mirrors the agent loop's Tool so a plugin's tools can be advertised to the
// model and dispatched by the host under the same manifest filter + egress
// gate. A local copy is used (instead of importing internal/agent) to keep the
// spec standalone and free of import cycles.
type Tool struct {
	Name string
	Exec func(args string) (string, error)
}

// AgentPlugin is the spec an external agent plugin implements. The host
// (airtapd) resolves a plugin by name via a registered Loader, advertises its
// Tools to the model (after manifest filtering), and calls Run to drive the
// agent's loop with the host-allowed tool set so every outbound call stays
// behind the moat.
type AgentPlugin interface {
	// Name is the plugin identifier (e.g. "aider", "cline", "continue").
	Name() string

	// Tools returns the tool set this plugin advertises to the model. The host
	// may further filter these by the manifest's agent.tools allowlist so an
	// operator restricting tools:[read] gets ONLY read — the same contract the
	// built-in loop honors.
	Tools() []Tool

	// Run drives the agent loop. It receives the run context, the user prompt,
	// and the manifest-filtered tool set the host allows. It MUST honor ctx
	// cancellation (client disconnect / SIGTERM — the host cancels ctx on conn
	// close) and route ALL model + subprocess egress through the host's egress
	// proxy + audit — never dial out directly. The host owns the moat; the
	// plugin owns the agent's workflow.
	Run(ctx context.Context, prompt string, tools []Tool) error
}

// Loader resolves a plugin by name. Registered with a Registry at host startup;
// concrete loaders (aider / cline / continue) are v0.4.0+ and out_of_scope for
// the v0.3.0 spec.
type Loader interface {
	Load(name string) (AgentPlugin, error)
}

// ErrUnknownPlugin is returned when no loader is registered for a name.
var ErrUnknownPlugin = errors.New("plugin: unknown agent plugin")

// Registry is the minimal loader registry stub. Hosts register loaders at
// startup; the loop resolves a plugin by name. Concrete loaders are v0.4.0+
// (out_of_scope for the v0.3.0 spec), so in v0.3.0 a fresh Registry resolves
// every name to ErrUnknownPlugin — the contract is in place, the wiring is not.
type Registry struct {
	loaders map[string]Loader
}

// NewRegistry returns an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{loaders: make(map[string]Loader)}
}

// Register associates name with a loader. A later Load(name) resolves it.
// Registering a name twice replaces the prior loader.
func (r *Registry) Register(name string, l Loader) {
	if r.loaders == nil {
		r.loaders = make(map[string]Loader)
	}
	r.loaders[name] = l
}

// Load resolves a registered plugin by name. It returns ErrUnknownPlugin
// (wrapped) when no loader is registered for name, so callers can
// errors.Is-check the sentinel.
func (r *Registry) Load(name string) (AgentPlugin, error) {
	if l, ok := r.loaders[name]; ok {
		return l.Load(name)
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, name)
}
