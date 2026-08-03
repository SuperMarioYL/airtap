// Package agent implements the on-box ReAct loop: system prompt -> model call ->
// tool dispatch -> loop until done. Per MVP plan §4 the loop runs on-box where
// the 国产 GPU + model live, so only tool-call results + diffs cross the tunnel.
package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/SuperMarioYL/airtap/internal/audit"
	"github.com/SuperMarioYL/airtap/internal/egress"
	"github.com/SuperMarioYL/airtap/internal/manifest"
	"github.com/SuperMarioYL/airtap/internal/model"
)

// MaxIterations bounds the ReAct loop to prevent runaway tool calls.
const MaxIterations = 8

// Loop is the on-box ReAct agent loop. It binds a manifest, a model client, the
// egress proxy (the sole network egress), and an audit log.
type Loop struct {
	manifest *manifest.Manifest
	client   *model.Client
	proxy    *egress.Proxy
	audit    *audit.Audit
	tools    []Tool // manifest-filtered tool set (fix-manifest-tools-not-honored)

	out io.Writer // streaming progress sink; defaults to os.Stdout
}

// NewLoop constructs a Loop. The proxy is installed as the process-wide HTTP
// dialer so every model call (and any other outbound HTTP) is gated by the
// egress allowlist. The dispatched tool set is filtered to m.Agent.Tools
// (fix-manifest-tools-not-honored): an operator who sets tools:[read] gets ONLY
// read advertised to the model and executed on dispatch — DefaultTools is no
// longer hardcoded.
func NewLoop(m *manifest.Manifest, c *model.Client, p *egress.Proxy, a *audit.Audit) *Loop {
	l := &Loop{
		manifest: m,
		client:   c,
		proxy:    p,
		audit:    a,
		out:      os.Stdout,
	}
	if m != nil {
		l.tools = filterTools(DefaultTools, m.Agent.Tools)
	}
	if p != nil {
		installEgress(p)
	}
	return l
}

// Tools returns the manifest-filtered tool set this loop advertises and
// dispatches. Exposed for tests and for airtapd startup logging.
func (l *Loop) Tools() []Tool {
	if l.tools == nil {
		return nil
	}
	out := make([]Tool, len(l.tools))
	copy(out, l.tools)
	return out
}

// Dispatch looks up a tool by name in the loop's manifest-filtered set and
// invokes it with args. Unknown / non-advertised tool names return an error so
// the loop can surface the failure back to the model as a tool-result. This is
// the filtered counterpart to the package-level Dispatch(DefaultTools).
func (l *Loop) Dispatch(name, args string) (string, error) {
	for _, t := range l.tools {
		if t.Name == name {
			return t.Exec(args)
		}
	}
	return "", fmt.Errorf("unknown or disabled tool: %s", name)
}

// SetOutput redirects the loop's streaming progress. The daemon uses this to pipe
// loop output over the mTLS connection to the thin client's TUI stream.
func (l *Loop) SetOutput(w io.Writer) {
	if w != nil {
		l.out = w
	}
}

// installEgress wires p.DialContext into the process-wide HTTP transport so
// that every outbound HTTP dial is gated by the allowlist AND honors the
// request context (fix-egress-dial-ignores-context): SIGTERM to airtapd and
// the model client's HTTPTimeout now propagate into an in-flight dial. If the
// default transport is not an *http.Transport (e.g. in tests), this is a no-op.
func installEgress(p *egress.Proxy) {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	t.DialContext = p.DialContext
}

// Run executes the ReAct loop: system prompt -> model.Chat -> dispatch tool calls
// -> repeat until the model returns a final answer (no tool calls) or
// MaxIterations is reached. Progress lines are streamed to l.out for the TUI.
//
// Message history follows the OpenAI tool-calling protocol: each assistant turn
// (with its tool_calls) is appended verbatim, then each tool result is appended
// as a role="tool" message referencing the originating tool_call_id.
func (l *Loop) Run(ctx context.Context, prompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.client == nil {
		return fmt.Errorf("agent: nil model client")
	}

	msgs := []model.Message{
		{Role: "system", Content: l.systemPrompt()},
		{Role: "user", Content: prompt},
	}
	tools := toModelTools(l.tools)

	l.stream("airtap: agent loop started, workdir=%s", l.workdir())

	for i := 1; i <= MaxIterations; i++ {
		// Honor cancellation between turns.
		if err := ctx.Err(); err != nil {
			return err
		}

		l.stream("airtap: model call %d", i)
		resp, err := l.client.Chat(msgs, tools)
		if err != nil {
			l.stream("airtap: model error: %v", err)
			return fmt.Errorf("agent: model chat: %w", err)
		}
		// Chat already guarantees at least one choice.
		assistant := resp.Choices[0].Message

		// Append the assistant turn (content + any tool_calls) verbatim so the
		// following tool messages can reference tool_call_id per OpenAI protocol.
		msgs = append(msgs, assistant)

		// No tool calls => the model produced its final answer.
		if len(assistant.ToolCalls) == 0 {
			if strings.TrimSpace(assistant.Content) != "" {
				l.stream("airtap: final answer:")
				l.stream("%s", assistant.Content)
			}
			l.stream("airtap: done")
			return nil
		}

		// Dispatch each tool call and append its result as a role="tool" message.
		for _, call := range assistant.ToolCalls {
			name := call.Function.Name
			args := call.Function.Arguments
			l.stream("airtap: tool: %s %s", name, args)
			out, derr := l.Dispatch(name, args)
			if derr != nil {
				l.stream("airtap: tool %s error: %v", name, derr)
				out = derr.Error()
			} else {
				l.stream("airtap: tool %s result: %s", name, truncate(out, 200))
			}
			msgs = append(msgs, model.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       name,
				Content:    out,
			})
		}
	}

	l.stream("airtap: max iterations reached")
	return fmt.Errorf("agent: max iterations (%d) reached", MaxIterations)
}

// systemPrompt builds the system message: who the agent is, where it runs, which
// tools it has, and that egress is air-gapped to the on-box model endpoint.
func (l *Loop) systemPrompt() string {
	var b strings.Builder
	b.WriteString("You are Airtap, an on-box coding agent running inside a 信创 GPU box. ")
	b.WriteString("You operate inside the repository at " + l.workdir() + ". ")
	b.WriteString("Use the provided tools to read, edit, list files, and run shell commands. ")
	b.WriteString("Prefer minimal, correct changes. ")
	b.WriteString("When the task is complete, reply with a short final summary and call no further tools. ")
	b.WriteString("All outbound network calls are restricted to the on-box model endpoint; do not attempt to reach the public internet.")
	if names := l.toolNames(); len(names) > 0 {
		b.WriteString(" Available tools: " + strings.Join(names, ", ") + ".")
	}
	return b.String()
}

// workdir returns the manifest-declared agent workdir, defaulting to ".".
func (l *Loop) workdir() string {
	if l.manifest == nil {
		return "."
	}
	if l.manifest.Agent.Workdir == "" {
		return "."
	}
	return l.manifest.Agent.Workdir
}

// toolNames returns the manifest-declared tool list (for the system prompt).
func (l *Loop) toolNames() []string {
	if l.manifest == nil {
		return nil
	}
	return l.manifest.Agent.Tools
}

// stream writes a progress line to the loop's output sink. It is the single
// streaming surface the daemon pipes over mTLS to the thin-client TUI.
func (l *Loop) stream(format string, args ...any) {
	if l.out == nil {
		return
	}
	fmt.Fprintf(l.out, format+"\n", args...)
}

// truncate caps a string to n runes (rune-safe so 中文 is not split mid-character).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// toModelTools converts the agent's executable tools into the model package's
// OpenAI tool declarations. Each tool becomes {"type":"function","function":{...}}
// matching model.Tool / model.ToolFunction exactly.
func toModelTools(ts []Tool) []model.Tool {
	out := make([]model.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, model.Tool{
			Type: "function",
			Function: model.ToolFunction{
				Name:        t.Name,
				Description: toolDescription(t.Name),
				Parameters:  toolSchema(t.Name),
			},
		})
	}
	return out
}
