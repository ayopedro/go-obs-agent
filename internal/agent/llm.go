package agent

import (
	"context"
	"encoding/json"
)

// Engine runs one investigation per call; Close releases provider resources.
type Engine interface {
	Investigate(ctx context.Context, prompt string) (string, error)
	Close() error
}

// ToolCall is a model request to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Input is either the opening prompt or the results of the previous tool calls.
type Input struct {
	Prompt  string
	Results []ToolResult
}

// Reply is one model turn.
type Reply struct {
	Text         string
	ToolCalls    []ToolCall
	InputTokens  int64
	OutputTokens int64
	Truncated    bool // output stopped at the token limit
}

// Session holds one conversation's provider-specific message history.
type Session interface {
	Send(ctx context.Context, in Input) (Reply, error)
}

// Provider creates conversations for a model API with function calling.
type Provider interface {
	NewSession(system string, tools []Tool) Session
}
