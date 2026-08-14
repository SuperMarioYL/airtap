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

// DefaultMaxIterations is the default ReAct loop cap when the manifest does not
// set agent.max_iterations (fix-max-iterations-too-low: 8 was too low for real
// coding tasks; 25 lets a typical read/edit/build/fix/re-run cycle complete).
const DefaultMaxIterations = 25

// MaxIterationsCeiling is the hard upper bound on agent.max_iterations so a typo
// (e.g. 999999) cannot trigger runaway GPU spend.
const MaxIterationsCeiling = 100

// chatClient is the model surface the loop depends on. *model.Client satisfies
// it; the interface lets tests drive Run with a fake that returns canned tool
// calls (fix-bash-tool-output-discarded-on-error regression coverage) without a
// live on-box model endpoint.
type chatClient interface {
	Chat(messages []model.Message, tools []model.Tool) (*model.ChatResponse, error)
}

// Loop is the on-box ReAct agent loop. It binds a manifest, a model client, the
// egress proxy (the sole network egress), and an audit log.
type Loop struct {
	manifest *manifest.Manifest
	client   chatClient
	proxy    *egress.Proxy
	audit    *audit.Audit
	tools    []Tool // manifest-filtered tool set (fix-manifest-tools-not-honored)
	maxIter  int    // ReAct cap; manifest-configurable (fix-max-iterations-too-low)

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
		proxy:    p,
		audit:    a,
		out:      os.Stdout,
		maxIter:  DefaultMaxIterations,
	}
	// Assign the concrete *model.Client to the chatClient interface only when
	// non-nil, so a nil client stays a nil interface (not a typed-nil) and the
	// Run nil-check below behaves correctly.
	if c != nil {
		l.client = c
	}
	if m != nil {
		l.tools = filterTools(DefaultTools, m.Agent.Tools)
		// fix-max-iterations-too-low: honor agent.max_iterations from the manifest.
		if n := m.Agent.MaxIterations; n > 0 {
			l.maxIter = n
			if l.maxIter > MaxIterationsCeiling {
				l.maxIter = MaxIterationsCeiling
			}
		}
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
// invokes it with args, threading the run context so a hanging command can be
// canceled (fix-bash-tool-uncancelable-hang). Unknown / non-advertised tool names
// return an error so the loop can surface the failure back to the model as a
// tool-result.
func (l *Loop) Dispatch(ctx context.Context, name, args string) (string, error) {
	for _, t := range l.tools {
		if t.Name == name {
			return t.Exec(ctx, args)
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

	// Defensive: a Loop constructed via a struct literal (e.g. in tests) may
	// have maxIter == 0; fall back to the default so the loop actually runs.
	maxIter := l.maxIter
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}

	l.stream("airtap: agent loop started, workdir=%s", l.workdir())

	for i := 1; i <= maxIter; i++ {
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
			out, derr := l.Dispatch(ctx, name, args)
			if derr != nil {
				l.stream("airtap: tool %s error: %v", name, derr)
				// fix-bash-tool-output-discarded-on-error: bashTool returns the
				// command's combined stdout/stderr as its first value alongside
				// a wrapped error (tools.go: `return string(out), fmt.Errorf("bash: %w", err)`).
				// Preserve that output for the model and append the error string
				// instead of overwriting it with a bare "bash: exit status 1" —
				// otherwise the ReAct loop never sees the compiler/test/grep
				// output it needs to debug the failure, breaking it precisely on
				// the failing-path cases (go build / go test) that matter most.
				if out == "" {
					out = derr.Error()
				} else {
					out = out + "\n" + derr.Error()
				}
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
	return fmt.Errorf("agent: max iterations (%d) reached", maxIter)
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
