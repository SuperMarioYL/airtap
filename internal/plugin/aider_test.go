package plugin

import (
	"context"
	"strings"
	"testing"
)

// fix-aider-plugin-ignores-onbox-endpoint (v0.5.0): the Aider wrapper must
// point aider at the on-box model endpoint (so it dials 127.0.0.1 behind the
// egress proxy + netns moat) instead of aider's default cloud endpoint, which
// the moat blocks by design on a 数据不出境 box. v0.4 spawned aider with no
// --model and no endpoint env, so the headline plugin could not complete a
// single task on the exact surface the product targets. This asserts the cmd
// construction carries the wiring (aider binary not required).
func TestAiderCmdTargetsOnBoxEndpoint(t *testing.T) {
	const endpoint = "http://127.0.0.1:8000/v1"
	const name = "deepseek-v3"
	cmd := buildAiderCmd(context.Background(), "fix server.go", endpoint, name)

	// --model <name> must be on the argv.
	gotModel := false
	for i, a := range cmd.Args {
		if a == "--model" && i+1 < len(cmd.Args) && cmd.Args[i+1] == name {
			gotModel = true
		}
	}
	if !gotModel {
		t.Fatalf("aider cmd argv should include --model %s; got %v", name, cmd.Args)
	}

	// Env must overlay the on-box OpenAI-compatible base URL.
	gotBase := false
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "OPENAI_API_BASE=") &&
			strings.TrimPrefix(e, "OPENAI_API_BASE=") == endpoint {
			gotBase = true
		}
	}
	if !gotBase {
		t.Fatalf("aider cmd env should set OPENAI_API_BASE=%s; got %v", endpoint, cmd.Env)
	}
}

// The loader threads the manifest's model endpoint + name into the plugin so
// Run() can wire aider to the on-box endpoint.
func TestAiderLoaderThreadsModelConfig(t *testing.T) {
	l := &AiderLoader{
		ModelEndpoint: "http://127.0.0.1:8000/v1",
		ModelName:     "qwen3-coder",
	}
	p, err := l.Load("aider")
	if err != nil {
		t.Fatalf("Load(aider): unexpected error: %v", err)
	}
	ap, ok := p.(*AiderPlugin)
	if !ok {
		t.Fatalf("expected *AiderPlugin, got %T", p)
	}
	if ap.modelEndpoint != "http://127.0.0.1:8000/v1" || ap.modelName != "qwen3-coder" {
		t.Fatalf("loader did not thread model config: got endpoint=%q name=%q",
			ap.modelEndpoint, ap.modelName)
	}
	// An unregistered name still fails with the sentinel.
	if _, err := l.Load("cline"); err == nil {
		t.Fatalf("Load(cline) on an AiderLoader should error")
	}
}
