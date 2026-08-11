package plugin

import (
	"context"
	"errors"
	"testing"
)

// stubPlugin is a minimal AgentPlugin for registry tests.
type stubPlugin struct{ name string }

func (s *stubPlugin) Name() string                                          { return s.name }
func (s *stubPlugin) Tools() []Tool                                         { return nil }
func (s *stubPlugin) Run(ctx context.Context, prompt string, tools []Tool) error { return nil }

// stubLoader returns its plugin only when the requested name matches.
type stubLoader struct{ p AgentPlugin }

func (l *stubLoader) Load(name string) (AgentPlugin, error) {
	if l.p != nil && l.p.Name() == name {
		return l.p, nil
	}
	return nil, errors.New("plugin: not found")
}

// A registered plugin resolves through Load.
func TestRegistryLoadRegistered(t *testing.T) {
	r := NewRegistry()
	want := &stubPlugin{name: "aider"}
	r.Register("aider", &stubLoader{p: want})

	got, err := r.Load("aider")
	if err != nil {
		t.Fatalf("Load(aider): unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("Load(aider): got nil plugin")
	}
	if got.Name() != "aider" {
		t.Fatalf("Load(aider): got name %q, want %q", got.Name(), "aider")
	}
}

// An unregistered name fails with the ErrUnknownPlugin sentinel
// (errors.Is-checkable), so the host can distinguish "no such plugin" from a
// loader-internal error.
func TestRegistryLoadUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Load("nope")
	if err == nil {
		t.Fatalf("Load(unknown): expected an error, got nil")
	}
	if !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("Load(unknown): expected ErrUnknownPlugin, got: %v", err)
	}
}

// A fresh registry (v0.3.0 default state) resolves every name to
// ErrUnknownPlugin — the spec/contract is in place, the wiring is not.
func TestRegistryEmptyResolvesAllUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Load("continue")
	if !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("fresh registry should resolve every name to ErrUnknownPlugin, got: %v", err)
	}
}
