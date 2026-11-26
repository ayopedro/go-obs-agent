package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ayopedro/go-obs-agent/internal/audit"
	"github.com/ayopedro/go-obs-agent/internal/redact"
)

const (
	maxTurns        = 15
	maxTokens       = 4096
	maxToolResult   = 64 << 10
	investigationTO = 5 * time.Minute
)

// toolbox runs tools by name for every provider and audits each call.
type toolbox struct {
	tools map[string]Tool
	order []Tool
}

func newToolbox(tools []Tool) *toolbox {
	b := &toolbox{tools: map[string]Tool{}, order: tools}
	for _, t := range tools {
		b.tools[t.Name] = t
	}
	return b
}

// run never fails the investigation: errors go back to the model as tool errors.
func (b *toolbox) run(ctx context.Context, name string, input []byte) (string, bool) {
	tool, ok := b.tools[name]
	if !ok {
		audit.Warn(ctx, "tool.unknown", "tool", name)
		return "unknown tool " + name, true
	}
	started := time.Now()
	out, err := tool.Call(ctx, input)
	attrs := []any{"tool", name, "args", audit.Truncate(string(input), 1000), "duration_ms", time.Since(started).Milliseconds(), "result_bytes", len(out)}
	if err != nil {
		msg, _ := redact.Text(err.Error())
		audit.Warn(ctx, "tool.call", append(attrs, "ok", false, "error", audit.Truncate(msg, 300))...)
		return "tool error: " + msg, true
	}
	// Telemetry can contain secrets or personal data; scrub before the model sees it.
	out, redactions := redact.Text(out)
	audit.Log(ctx, "tool.call", append(attrs, "ok", true, "redactions", redactions)...)
	if len(out) > maxToolResult {
		out = out[:maxToolResult] + `... [truncated: result exceeded 64 KiB]`
	}
	return out, false
}

// Start builds the engine for cfg.Provider with telemetry and allowed MCP tools.
func Start(ctx context.Context, cfg Config) (Engine, error) {
	servers, err := LoadMCPConfig(cfg.MCPConfigPath)
	if err != nil {
		return nil, err
	}
	mcpTools, closer, err := ConnectMCP(ctx, servers, os.Getenv)
	if err != nil {
		return nil, err
	}
	box := newToolbox(append(TelemetryTools(nil), mcpTools...))
	switch cfg.Provider {
	case "copilot":
		e, err := startCopilot(ctx, cfg, box, closer)
		if err != nil {
			closer()
		}
		return e, err
	case "azure":
		p, err := newAzureProvider(cfg)
		if err != nil {
			closer()
			return nil, err
		}
		return newLoopEngine(cfg.Model, p, box, closer), nil
	case "anthropic":
		return newLoopEngine(cfg.Model, newAnthropicProvider(cfg), box, closer), nil
	}
	closer()
	return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
}

// loopEngine runs a bounded tool-use loop. Each Investigate starts a fresh conversation.
type loopEngine struct {
	model    string
	provider Provider
	box      *toolbox
	close    func()
}

func newLoopEngine(model string, p Provider, box *toolbox, closer func()) *loopEngine {
	return &loopEngine{model: model, provider: p, box: box, close: closer}
}

func (e *loopEngine) Close() error {
	if e.close != nil {
		e.close()
	}
	return nil
}

func (e *loopEngine) Investigate(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, investigationTO)
	defer cancel()
	prompt, _ = redact.Text(prompt)
	session := e.provider.NewSession(instructions, e.box.order)
	started := time.Now()
	audit.Log(ctx, "agent.started", append(audit.Prompt(prompt), "model", e.model, "tools", len(e.box.order))...)
	var inTok, outTok int64
	toolCalls := 0
	done := func(outcome string, turns int) {
		audit.Log(ctx, "agent.completed", "outcome", outcome, "turns", turns, "tool_calls", toolCalls,
			"input_tokens", inTok, "output_tokens", outTok, "duration_ms", time.Since(started).Milliseconds())
	}
	in := Input{Prompt: prompt}
	for turn := 1; turn <= maxTurns; turn++ {
		reply, err := session.Send(ctx, in)
		if err != nil {
			done("model_error", turn)
			return "", err
		}
		inTok += reply.InputTokens
		outTok += reply.OutputTokens
		if len(reply.ToolCalls) == 0 {
			text, err := finalText(reply)
			if err != nil {
				done("no_response", turn)
			} else {
				done("answered", turn)
			}
			return text, err
		}
		in = Input{}
		for _, call := range reply.ToolCalls {
			toolCalls++
			content, isErr := e.box.run(ctx, call.Name, call.Input)
			in.Results = append(in.Results, ToolResult{CallID: call.ID, Content: content, IsError: isErr})
		}
	}
	done("max_turns", maxTurns)
	return "", fmt.Errorf("investigation did not finish within %d model turns", maxTurns)
}

func finalText(r Reply) (string, error) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return "", fmt.Errorf("agent completed without a response")
	}
	if r.Truncated {
		text += "\n\n[Response was cut off at the output token limit.]"
	}
	return text, nil
}
