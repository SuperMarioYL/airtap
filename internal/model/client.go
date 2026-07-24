// Package model is an OpenAI-compatible chat-completions client pointed at
// the on-box model endpoint. It speaks the standard /v1/chat/completions
// request/response shape including tool_calls, so any 国产 model served
// behind a vLLM-Ascend / MindIE OpenAI shim (DeepSeek-V3, Qwen3-Coder,
// GLM-4.6) is callable without per-model glue.
package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPTimeout caps each chat round-trip. On-box latency is dominated by
// generation, so this is generous.
const HTTPTimeout = 5 * time.Minute

// Message is a single chat message. The struct covers system/user/
// assistant/tool roles; assistant tool_calls and tool responses are
// carried in the corresponding fields.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=="tool" only
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a single tool invocation emitted by the assistant.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function Function `json:"function"`
}

// Function is the name + JSON-string arguments of a tool call.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string per OpenAI spec
}

// Tool declares a function the model may call.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the schema-level description of a tool.
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// ChatResponse is the parsed /v1/chat/completions response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object,omitempty"`
	Model   string   `json:"model,omitempty"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice is one completion alternative. The agent loop reads the first.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage reports token counts for the round-trip.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Client is a stateless chat-completions client bound to one endpoint
// and model name.
type Client struct {
	endpoint   string
	modelName  string
	httpClient *http.Client
}

// NewClient returns a Client for the given OpenAI-compatible endpoint
// (e.g. "http://127.0.0.1:8000/v1") and model name.
func NewClient(endpoint, modelName string) *Client {
	return &Client{
		endpoint:  strings.TrimRight(endpoint, "/"),
		modelName: modelName,
		httpClient: &http.Client{
			Timeout: HTTPTimeout,
		},
	}
}

// chatRequest is the wire shape sent to /v1/chat/completions.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

// chatError mirrors the standard OpenAI error envelope so callers can
// surface the upstream message.
type chatError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat sends a chat-completions request with the given message history
// and tool declarations, returning the assistant's response (which may
// contain content, tool_calls, or both).
func (c *Client) Chat(messages []Message, tools []Tool) (*ChatResponse, error) {
	body := chatRequest{
		Model:    c.modelName,
		Messages: messages,
		Tools:    tools,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("model: marshal request: %w", err)
	}
	url := c.endpoint + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("model: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ce chatError
		_ = json.Unmarshal(raw, &ce)
		if ce.Error.Message != "" {
			return nil, fmt.Errorf("model: %d %s: %s", resp.StatusCode, ce.Error.Type, ce.Error.Message)
		}
		return nil, fmt.Errorf("model: %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}

	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("model: parse response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("model: response has no choices")
	}
	return &out, nil
}

// truncate clips a string to n runes for error messages.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
