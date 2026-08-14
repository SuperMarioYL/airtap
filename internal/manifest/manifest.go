// Package manifest parses and validates an Airtap EgressManifest
// (airtap.yaml), the declarative contract that binds the box address,
// on-box model endpoint, egress policy, and agent runtime config.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Valid egress policy modes.
const (
	// ModeAirgap denies every outbound dial not listed in egress.allow.
	// allow MUST be non-empty in this mode.
	ModeAirgap = "airgap"
	// ModeAllowlist is the looser policy: dials in allow are permitted,
	// others are logged-and-denied. allow MAY be empty (deny-all).
	ModeAllowlist = "allowlist"
)

// validTools is the set of tool names the agent loop knows how to advertise
// and dispatch. fix-manifest-tools-not-honored: an unknown name in
// agent.tools must surface as a startup validation error rather than silently
// dropping to a partial (or empty) tool set mid-run.
var validTools = map[string]bool{
	"read":  true,
	"write": true,
	"list":  true,
	"bash":  true,
}

// Box describes the GPU box the thin client connects to over mTLS.
type Box struct {
	Addr string `yaml:"addr"` // host:port, e.g. 10.0.0.5:7437
	TLS  string `yaml:"tls"`  // handshake kind, must be "mTLS" for v0.1
	CA   string `yaml:"ca"`   // path to CA PEM used to verify the peer
}

// Model describes the on-box OpenAI-compatible model endpoint. The
// endpoint is served on 127.0.0.1 on the GPU box so model calls never
// leave the box.
type Model struct {
	Endpoint string `yaml:"endpoint"` // e.g. http://127.0.0.1:8000/v1
	Name     string `yaml:"name"`      // e.g. deepseek-v3
}

// Egress describes the outbound dial policy the daemon enforces. The
// daemon is the only network egress from the box; every attempt
// (allowed or denied) is appended to Audit.
type Egress struct {
	Allow []string `yaml:"allow"` // permitted dial targets, host:port
	Mode  string   `yaml:"mode"`  // airgap | allowlist
	Audit string   `yaml:"audit"` // path to append-only audit log
}

// AgentCfg describes the agent runtime environment on the box.
type AgentCfg struct {
	Workdir      string   `yaml:"workdir"`       // repo root the agent edits
	Tools        []string `yaml:"tools"`        // enabled tools: read,write,list,bash
	MaxIterations int      `yaml:"max_iterations"` // v0.4.0: optional ReAct cap; 0 => default 25, ceiling 100
}

// Manifest is the parsed, validated EgressManifest contract. It is the
// single source of truth shared (out of band) between the thin client
// and airtapd.
type Manifest struct {
	Box    Box      `yaml:"box"`
	Model  Model    `yaml:"model"`
	Egress Egress   `yaml:"egress"`
	Agent  AgentCfg `yaml:"agent"`
}

// Load reads, parses, and validates an airtap.yaml manifest at path.
// It is the entry point used by both the thin client and the daemon.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	m := &Manifest{}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("manifest: invalid %s: %w", path, err)
	}
	return m, nil
}

// Validate checks the manifest against the EgressManifest schema rules:
// required fields present, egress.mode in {airgap, allowlist}, and allow
// non-empty when mode=airgap. It returns a single error aggregating all
// violations, so the caller can surface the full list at once.
func (m *Manifest) Validate() error {
	if m == nil {
		return errors.New("manifest: nil manifest")
	}
	var errs []string

	if strings.TrimSpace(m.Box.Addr) == "" {
		errs = append(errs, "box.addr is required")
	}
	if strings.TrimSpace(m.Box.TLS) == "" {
		errs = append(errs, "box.tls is required")
	}
	if strings.TrimSpace(m.Box.CA) == "" {
		errs = append(errs, "box.ca is required")
	}

	if strings.TrimSpace(m.Model.Endpoint) == "" {
		errs = append(errs, "model.endpoint is required")
	}
	if strings.TrimSpace(m.Model.Name) == "" {
		errs = append(errs, "model.name is required")
	}

	switch m.Egress.Mode {
	case ModeAirgap, ModeAllowlist:
		// ok
	default:
		errs = append(errs, fmt.Sprintf("egress.mode must be one of {airgap,allowlist}, got %q", m.Egress.Mode))
	}
	if m.Egress.Mode == ModeAirgap && len(m.Egress.Allow) == 0 {
		errs = append(errs, "egress.allow must be non-empty when mode=airgap")
	}
	if strings.TrimSpace(m.Egress.Audit) == "" {
		errs = append(errs, "egress.audit is required")
	}

	if strings.TrimSpace(m.Agent.Workdir) == "" {
		errs = append(errs, "agent.workdir is required")
	}
	if len(m.Agent.Tools) == 0 {
		errs = append(errs, "agent.tools must be non-empty")
	}
	// Reject unknown tool names at startup so a typo (e.g. "reed") fails
	// fast instead of silently advertising an empty/partial tool set.
	for _, t := range m.Agent.Tools {
		if !validTools[t] {
			errs = append(errs, fmt.Sprintf("agent.tools: unknown tool %q (valid: read, write, list, bash)", t))
		}
	}
	// v0.4.0: max_iterations is optional (0 => default), but a negative value
	// is a startup error so a typo doesn't silently disable the cap.
	if m.Agent.MaxIterations < 0 {
		errs = append(errs, "agent.max_iterations must be >= 0 (0 uses the default)")
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation: %s", strings.Join(errs, "; "))
	}
	return nil
}
