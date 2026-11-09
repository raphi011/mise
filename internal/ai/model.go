package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Model is a chat model behind a provider API. The tool loop talks to it
// through provider-neutral requests and responses.
type Model interface {
	// Chat sends one request and returns the model's turn.
	Chat(ctx context.Context, req Request) (Response, error)
	// Test sends one tiny request and reports whether the model answered.
	Test(ctx context.Context) error
}

// Role is who wrote a message.
type Role string

// Roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Request is one model call.
type Request struct {
	// System is the system prompt. It never holds untrusted text.
	System   string
	Messages []Message
	Tools    []Tool
	// MaxTokens caps the reply.
	MaxTokens int
}

// Message is one turn of the conversation.
type Message struct {
	Role Role
	// Text is the turn's text: the user's input, or what the model said.
	Text string
	// ToolCalls are the model's tool calls (assistant turns).
	ToolCalls []ToolCall
	// ToolResults answer the previous turn's calls (user turns).
	ToolResults []ToolResult

	// raw is the provider's own form of an assistant turn, replayed
	// unchanged so thinking blocks keep their signatures.
	raw any
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is a tool's answer to a call.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Tool describes a tool to the model. Every tool is strict: Properties
// and Required form a JSON Schema object without additional properties;
// nested objects must say so themselves.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// schema returns the tool's input schema as a JSON Schema object.
func (t Tool) schema() map[string]any {
	props := t.Properties
	if props == nil {
		props = map[string]any{}
	}
	req := t.Required
	if req == nil {
		req = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

// StopReason says why the model stopped.
type StopReason string

// Stop reasons.
const (
	// StopEnd: the model finished its turn.
	StopEnd StopReason = "end"
	// StopToolUse: the model wants tool results.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens: the reply hit MaxTokens.
	StopMaxTokens StopReason = "max_tokens"
	// StopRefusal: the provider declined the request.
	StopRefusal StopReason = "refusal"
)

// Response is the model's turn.
type Response struct {
	// Message is the assistant turn, to append to the conversation as is.
	Message Message
	Stop    StopReason
	Usage   Usage
}

// Usage counts tokens of one call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ProviderError is an error the provider reported, or a failure to reach
// it. Message is shown to the user under "See details".
type ProviderError struct {
	// Status is the HTTP status, 0 when the provider was not reached.
	Status  int
	Message string
}

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return "ai: provider: " + e.Message
	}
	return fmt.Sprintf("ai: provider: %d %s", e.Status, e.Message)
}

// providerError wraps err unless it is the context ending, which callers
// report as cancellation or the job's time limit.
func providerError(ctx context.Context, err error, status int, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if message == "" {
		message = err.Error()
	}
	return &ProviderError{Status: status, Message: message}
}

// testRequest is the connection test: one short reply, with a tool so a
// local model without tool support fails the test.
var testRequest = Request{
	System:    "You are checking a connection. Answer with one word.",
	Messages:  []Message{{Role: RoleUser, Text: "Reply with OK."}},
	Tools:     []Tool{{Name: "noop", Description: "Does nothing. Do not call it."}},
	MaxTokens: 256,
}
