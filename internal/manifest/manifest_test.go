package manifest

import "testing"

// fix-manifest-tools-not-honored: unknown tool names must surface as a
// startup validation error so a typo ("reed") fails fast rather than silently
// advertising an empty/partial tool set mid-run.
func TestValidateRejectsUnknownTool(t *testing.T) {
	m := &Manifest{
		Box:    Box{Addr: "127.0.0.1:7437", TLS: "mTLS", CA: "ca.pem"},
		Model:  Model{Endpoint: "http://127.0.0.1:8000/v1", Name: "deepseek-v3"},
		Egress: Egress{Allow: []string{"127.0.0.1:8000"}, Mode: ModeAirgap, Audit: "audit.log"},
		Agent:  AgentCfg{Workdir: ".", Tools: []string{"read", "reed", "bash"}},
	}
	err := m.Validate()
	if err == nil {
		t.Fatalf("expected validation error for unknown tool, got nil")
	}
}

func TestValidateAcceptsKnownTools(t *testing.T) {
	m := &Manifest{
		Box:    Box{Addr: "127.0.0.1:7437", TLS: "mTLS", CA: "ca.pem"},
		Model:  Model{Endpoint: "http://127.0.0.1:8000/v1", Name: "deepseek-v3"},
		Egress: Egress{Allow: []string{"127.0.0.1:8000"}, Mode: ModeAirgap, Audit: "audit.log"},
		Agent:  AgentCfg{Workdir: ".", Tools: []string{"read", "write", "list", "bash"}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid manifest, got: %v", err)
	}
}

func TestValidateRejectsEmptyTools(t *testing.T) {
	m := &Manifest{
		Box:    Box{Addr: "127.0.0.1:7437", TLS: "mTLS", CA: "ca.pem"},
		Model:  Model{Endpoint: "http://127.0.0.1:8000/v1", Name: "deepseek-v3"},
		Egress: Egress{Allow: []string{"127.0.0.1:8000"}, Mode: ModeAirgap, Audit: "audit.log"},
		Agent:  AgentCfg{Workdir: ".", Tools: []string{}},
	}
	if err := m.Validate(); err == nil {
		t.Fatalf("expected validation error for empty tools, got nil")
	}
}
