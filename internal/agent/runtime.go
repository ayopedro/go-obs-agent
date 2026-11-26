package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/ayopedro/go-obs-agent/internal/audit"
)

const (
	maxTurns        = 15
	maxTokens       = 4096
	maxToolResult   = 64 << 10
	investigationTO = 5 * time.Minute
)

// messenger is the part of the Anthropic client the loop needs; tests fake it.
type messenger interface {
	New(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

type sdkMessenger struct{ client anthropic.Client }

func (m sdkMessenger) New(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
	return m.client.Messages.New(ctx, p)
}

// Runtime runs a bounded tool-use loop against Claude. Each Investigate call
// starts a fresh conversation.
type Runtime struct {
	model string
	llm   messenger
	tools map[string]Tool
	order []Tool
	close func()
}

func newRuntime(model string, llm messenger, tools []Tool, closer func()) *Runtime {
	r := &Runtime{model: model, llm: llm, tools: map[string]Tool{}, order: tools, close: closer}
	for _, t := range tools {
		r.tools[t.Name] = t
	}
	return r
}

func Start(ctx context.Context, cfg Config) (*Runtime, error) {
	servers, err := LoadMCPConfig(cfg.MCPConfigPath)
	if err != nil {
		return nil, err
	}
	tools := TelemetryTools(nil)
	mcpTools, closer, err := ConnectMCP(ctx, servers, os.Getenv)
	if err != nil {
		return nil, err
	}
	tools = append(tools, mcpTools...)
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return newRuntime(cfg.Model, sdkMessenger{anthropic.NewClient(opts...)}, tools, closer), nil
}

func (r *Runtime) Close() error {
	if r.close != nil {
		r.close()
	}
	return nil
}

func (r *Runtime) toolParams() []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(r.order))
	for _, t := range r.order {
		schema := anthropic.ToolInputSchemaParam{Properties: t.Schema["properties"]}
		if req, ok := t.Schema["required"].([]any); ok {
			for _, v := range req {
				if s, ok := v.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema}})
	}
	return out
}

func (r *Runtime) Investigate(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, investigationTO)
	defer cancel()
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	tools := r.toolParams()
	started := time.Now()
	audit.Log(ctx, "agent.started", append(audit.Prompt(prompt), "model", r.model, "tools", len(tools))...)
	var inTok, outTok int64
	toolCalls := 0
	done := func(outcome string, turns int) {
		audit.Log(ctx, "agent.completed", "outcome", outcome, "turns", turns, "tool_calls", toolCalls,
			"input_tokens", inTok, "output_tokens", outTok, "duration_ms", time.Since(started).Milliseconds())
	}
	for turn := 1; turn <= maxTurns; turn++ {
		resp, err := r.llm.New(ctx, anthropic.MessageNewParams{
			Model: anthropic.Model(r.model), MaxTokens: maxTokens,
			System:   []anthropic.TextBlockParam{{Text: instructions}},
			Messages: messages, Tools: tools,
		})
		if err != nil {
			done("model_error", turn)
			return "", err
		}
		inTok += resp.Usage.InputTokens
		outTok += resp.Usage.OutputTokens
		messages = append(messages, resp.ToParam())
		if resp.StopReason != anthropic.StopReasonToolUse {
			text, err := finalText(resp)
			if err != nil {
				done("no_response", turn)
			} else {
				done("answered", turn)
			}
			return text, err
		}
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			use := block.AsToolUse()
			toolCalls++
			content, isErr := r.runTool(ctx, use.Name, use.Input)
			results = append(results, anthropic.NewToolResultBlock(use.ID, content, isErr))
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	done("max_turns", maxTurns)
	return "", fmt.Errorf("investigation did not finish within %d model turns", maxTurns)
}

// runTool never fails the investigation: errors go back to the model as tool errors.
func (r *Runtime) runTool(ctx context.Context, name string, input []byte) (string, bool) {
	tool, ok := r.tools[name]
	if !ok {
		audit.Warn(ctx, "tool.unknown", "tool", name)
		return "unknown tool " + name, true
	}
	started := time.Now()
	out, err := tool.Call(ctx, input)
	attrs := []any{"tool", name, "args", audit.Truncate(string(input), 1000), "duration_ms", time.Since(started).Milliseconds(), "result_bytes", len(out)}
	if err != nil {
		audit.Warn(ctx, "tool.call", append(attrs, "ok", false, "error", audit.Truncate(err.Error(), 300))...)
		return "tool error: " + err.Error(), true
	}
	audit.Log(ctx, "tool.call", append(attrs, "ok", true)...)
	if len(out) > maxToolResult {
		out = out[:maxToolResult] + `... [truncated: result exceeded 64 KiB]`
	}
	return out, false
}

func finalText(resp *anthropic.Message) (string, error) {
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.AsText().Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", fmt.Errorf("agent completed without a response (stop reason %q)", resp.StopReason)
	}
	if resp.StopReason == anthropic.StopReasonMaxTokens {
		text += "\n\n[Response was cut off at the output token limit.]"
	}
	return text, nil
}
