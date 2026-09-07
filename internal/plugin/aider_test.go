package plugin

import (
	"context"
	"strings"
	"testing"
)

// fix-aider-plugin-ignores-onbox-endpoint (v0.5.0) + fix-aider-netns-
// loopback-down-blocks-onbox-model (v0.6.0): the Aider wrapper must point
// aider at the on-box model endpoint (so it dials 127.0.0.1 behind the
// egress proxy + netns moat) instead of aider's default cloud endpoint,
// AND it must bring the netns loopback UP so that dial actually reaches the
// on-box model — a fresh CLONE_NEWNET netns leaves lo DOWN by kernel default,
// so the v0.5.0 endpoint wiring never worked inside the moat. This asserts
// the cmd construction carries both wirings (aider binary not required).
func TestAiderCmdTargetsOnBoxEndpoint(t *testing.T) {
	const endpoint = "http://127.0.0.1:8000/v1"
	const name = "deepseek-v3"
	const prompt = "fix server.go"
	cmd := buildAiderCmd(context.Background(), prompt, endpoint, name)

	// v0.6.0: the cmd runs via `sh -c <script>` so the script can bring the
	// netns loopback up before exec-ing aider. The prompt + model name travel
	// via env, so the static script must NOT inline user-controlled text.
	if len(cmd.Args) < 3 || cmd.Args[0] != "sh" || cmd.Args[1] != "-c" {
		t.Fatalf("expected sh -c <script>, got args %v", cmd.Args)
	}
	script := cmd.Args[2]
	if !strings.Contains(script, "ip link set lo up") {
		t.Fatalf("aider script should bring the netns loopback up (v0.6.0 fix); got %q", script)
	}
	if !strings.Contains(script, `--model "$AIRTAP_MODEL"`) {
		t.Fatalf("aider script should pass the model via the $AIRTAP_MODEL env, got %q", script)
	}
	if !strings.Contains(script, `--message "$AIRTAP_PROMPT"`) {
		t.Fatalf("aider script should pass the prompt via the $AIRTAP_PROMPT env (never inlined), got %q", script)
	}

	// Env must carry the on-box OpenAI-compatible base URL + the prompt/model
	// values the script expands (no inline user-controlled text).
	gotBase, gotPrompt, gotModel := false, false, false
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, "OPENAI_API_BASE=") && strings.TrimPrefix(e, "OPENAI_API_BASE=") == endpoint:
			gotBase = true
		case strings.HasPrefix(e, "AIRTAP_PROMPT=") && strings.TrimPrefix(e, "AIRTAP_PROMPT=") == prompt:
			gotPrompt = true
		case strings.HasPrefix(e, "AIRTAP_MODEL=") && strings.TrimPrefix(e, "AIRTAP_MODEL=") == name:
			gotModel = true
		}
	}
	if !gotBase {
		t.Fatalf("aider cmd env should set OPENAI_API_BASE=%s; got %v", endpoint, cmd.Env)
	}
	if !gotPrompt {
		t.Fatalf("aider cmd env should set AIRTAP_PROMPT=%s; got %v", prompt, cmd.Env)
	}
	if !gotModel {
		t.Fatalf("aider cmd env should set AIRTAP_MODEL=%s; got %v", name, cmd.Env)
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
