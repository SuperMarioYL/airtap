package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/SuperMarioYL/airtap/internal/model"
)

// fakeChatClient drives the loop's Run with canned model responses so the
// tool-call dispatch path (loop.go dispatch block) can be exercised without a
// live on-box model endpoint. On the first Chat it emits one tool call; on the
// second it captures the tool-result message the loop appended and returns a
// final answer (no tool calls) so Run terminates.
type fakeChatClient struct {
	toolName string
	toolArgs string
	final    string
	calls    int
	toolMsg  model.Message // captured on the second call
}

func (f *fakeChatClient) Chat(messages []model.Message, tools []model.Tool) (*model.ChatResponse, error) {
	f.calls++
	if f.calls == 1 {
		// Emit one tool call for the loop to dispatch.
		return &model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						Role: "assistant",
						ToolCalls: []model.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: model.Function{
									Name:      f.toolName,
									Arguments: f.toolArgs,
								},
							},
						},
					},
				},
			},
		}, nil
	}
	// Capture the tool-result message the loop appended so the test can assert
	// exactly what the model would see.
	for _, m := range messages {
		if m.Role == "tool" {
			f.toolMsg = m
		}
	}
	// Final answer (no tool calls) so Run terminates.
	return &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{
					Role:    "assistant",
					Content: f.final,
				},
			},
		},
	}, nil
}

// fix-bash-tool-output-discarded-on-error: when a tool returns BOTH a non-empty
// output AND an error (bashTool on a non-zero exit: `return string(out),
// fmt.Errorf("bash: %w", err)`), the role="tool" message the model receives
// must carry the real command output PLUS the exit status — not the bare
// "bash: exit status 1" the old overwrite (`out = derr.Error()`) produced.
func TestLoopPreservesToolOutputOnToolError(t *testing.T) {
	const wantOut = "main.go:12: undefined: foo\nmain.go:13: undefined: bar"
	execFn := func(args string) (string, error) {
		return wantOut, fmt.Errorf("bash: exit 1")
	}
	fc := &fakeChatClient{toolName: "bash", toolArgs: "go build ./...", final: "fixed"}
	l := &Loop{
		client: fc,
		tools:  []Tool{{Name: "bash", Exec: execFn}},
		out:    io.Discard,
	}
	if err := l.Run(context.Background(), "build it"); err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if fc.toolMsg.Role != "tool" {
		t.Fatalf("expected a role=tool message captured, got %+v", fc.toolMsg)
	}
	// The model must see the real command output...
	if !strings.Contains(fc.toolMsg.Content, wantOut) {
		t.Fatalf("tool message lost the command output:\nwant substring %q\ngot  %q", wantOut, fc.toolMsg.Content)
	}
	// ...AND the exit status / error string...
	if !strings.Contains(fc.toolMsg.Content, "bash: exit 1") {
		t.Fatalf("tool message lost the error string: got %q", fc.toolMsg.Content)
	}
	// ...and NOT be reduced to just the error (the old buggy behavior).
	if fc.toolMsg.Content == "bash: exit 1" {
		t.Fatalf("regression: tool message is ONLY the error string — the command output was discarded (old buggy behavior)")
	}
	// Exact contract: out + "\n" + err.Error().
	want := wantOut + "\n" + "bash: exit 1"
	if fc.toolMsg.Content != want {
		t.Fatalf("content mismatch:\nwant %q\ngot  %q", want, fc.toolMsg.Content)
	}
}

// Companion case: when the tool returns an EMPTY output alongside an error
// (e.g. bashTool fail-closed when netns isolation is unavailable), the message
// falls back to the bare error string (no trailing newline, no empty prefix).
func TestLoopFallsBackToErrorWhenToolOutputEmpty(t *testing.T) {
	execFn := func(args string) (string, error) {
		return "", fmt.Errorf("bash: netns isolation unavailable")
	}
	fc := &fakeChatClient{toolName: "bash", toolArgs: "echo hi", final: "ok"}
	l := &Loop{client: fc, tools: []Tool{{Name: "bash", Exec: execFn}}, out: io.Discard}
	if err := l.Run(context.Background(), "run"); err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	const want = "bash: netns isolation unavailable"
	if fc.toolMsg.Content != want {
		t.Fatalf("empty-output case: content = %q; want %q", fc.toolMsg.Content, want)
	}
}

// A tool that succeeds (no error) must pass its output through unchanged —
// the append logic must only run on the derr != nil branch.
func TestLoopPassesThroughToolOutputOnSuccess(t *testing.T) {
	execFn := func(args string) (string, error) {
		return "ok output", nil
	}
	fc := &fakeChatClient{toolName: "read", toolArgs: "/dev/null", final: "done"}
	l := &Loop{client: fc, tools: []Tool{{Name: "read", Exec: execFn}}, out: io.Discard}
	if err := l.Run(context.Background(), "read"); err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if fc.toolMsg.Content != "ok output" {
		t.Fatalf("success case: content = %q; want %q", fc.toolMsg.Content, "ok output")
	}
}
