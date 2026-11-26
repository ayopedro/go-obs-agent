package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ayopedro/go-obs-agent/internal/audit"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

func get(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig(t *testing.T) {
	for _, ok := range []map[string]string{
		{"LLM_PROVIDER": "copilot"},
		{"LLM_PROVIDER": "copilot", "LLM_MODEL": "m", "COPILOT_GITHUB_TOKEN": "t"},
		{"LLM_PROVIDER": "azure", "LLM_MODEL": "gpt-deploy", "LLM_BASE_URL": "https://r.openai.azure.com/"},
		{"LLM_PROVIDER": "anthropic", "LLM_MODEL": "m", "LLM_API_KEY": "k"},
	} {
		if _, err := LoadConfig(get(ok)); err != nil {
			t.Errorf("rejected %v: %v", ok, err)
		}
	}
	c, _ := LoadConfig(get(map[string]string{"LLM_PROVIDER": "azure", "LLM_MODEL": "d", "LLM_BASE_URL": "https://r.openai.azure.com/"}))
	if c.BaseURL != "https://r.openai.azure.com" {
		t.Errorf("endpoint not normalized: %q", c.BaseURL)
	}
	for _, bad := range []map[string]string{
		{},
		{"LLM_PROVIDER": "gemini"},
		{"LLM_PROVIDER": "copilot", "LLM_API_KEY": "k"},
		{"LLM_PROVIDER": "azure", "LLM_MODEL": "d"},
		{"LLM_PROVIDER": "azure", "LLM_MODEL": "d", "LLM_BASE_URL": "http://r.openai.azure.com"},
		{"LLM_PROVIDER": "azure", "LLM_MODEL": "d", "LLM_BASE_URL": "https://u:p@r.openai.azure.com"},
		{"LLM_PROVIDER": "anthropic", "LLM_MODEL": "m"},
	} {
		if _, err := LoadConfig(get(bad)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// fakeProvider replays scripted replies and records what the loop sent back.
type fakeProvider struct {
	replies []Reply
	inputs  []Input
}

func (f *fakeProvider) NewSession(string, []Tool) Session { return f }
func (f *fakeProvider) Send(_ context.Context, in Input) (Reply, error) {
	f.inputs = append(f.inputs, in)
	return f.replies[len(f.inputs)-1], nil
}

func call(name string) Reply {
	return Reply{ToolCalls: []ToolCall{{ID: "t1", Name: name, Input: json.RawMessage(`{"x":1}`)}}}
}

var answer = Reply{Text: "root cause: pool exhausted", InputTokens: 10, OutputTokens: 5}

func echoTool() Tool {
	return Tool{Name: "echo", Schema: map[string]any{"type": "object"}, Call: func(_ context.Context, in json.RawMessage) (string, error) { return "got " + string(in), nil }}
}

func TestInvestigateRunsToolsAndReturnsAnswer(t *testing.T) {
	llm := &fakeProvider{replies: []Reply{call("echo"), call("missing"), answer}}
	e := newLoopEngine("m", llm, newToolbox([]Tool{echoTool()}), nil)
	out, err := e.Investigate(t.Context(), "go")
	if err != nil || out != "root cause: pool exhausted" {
		t.Fatalf("%q %v", out, err)
	}
	if r := llm.inputs[1].Results[0]; r.IsError || r.Content != `got {"x":1}` || r.CallID != "t1" {
		t.Fatalf("tool result: %+v", r)
	}
	if r := llm.inputs[2].Results[0]; !r.IsError || !strings.Contains(r.Content, "unknown tool") {
		t.Fatalf("unknown tool not reported as error: %+v", r)
	}
}

func TestInvestigateStopsAfterMaxTurns(t *testing.T) {
	llm := &fakeProvider{}
	for range maxTurns {
		llm.replies = append(llm.replies, call("echo"))
	}
	if _, err := newLoopEngine("m", llm, newToolbox([]Tool{echoTool()}), nil).Investigate(t.Context(), "go"); err == nil {
		t.Fatal("loop did not stop")
	}
}

func TestInvestigationIsAudited(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	llm := &fakeProvider{replies: []Reply{call("echo"), answer}}
	ctx := audit.WithActor(t.Context(), audit.Actor{Platform: "slack", User: "U123"})
	if _, err := newLoopEngine("m", llm, newToolbox([]Tool{echoTool()}), nil).Investigate(ctx, "secret incident text"); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	for _, want := range []string{`"event":"agent.started"`, `"event":"tool.call"`, `"tool":"echo"`, `"event":"agent.completed"`, `"outcome":"answered"`, `"user":"U123"`, `"tool_calls":1`, `"output_tokens":5`} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %s in audit log:\n%s", want, log)
		}
	}
	if strings.Contains(log, "secret incident text") {
		t.Error("prompt text logged without opt-in")
	}
}

func TestTelemetryToolSchemas(t *testing.T) {
	tools := TelemetryTools(nil)
	if len(tools) != 3 || tools[0].Name != "analyze_traces" {
		t.Fatalf("tools: %+v", tools)
	}
	if req, _ := tools[0].Schema["required"].([]any); len(req) != 1 || req[0] != "trace_id" {
		t.Fatalf("required: %v", tools[0].Schema["required"])
	}
}

func TestCopilotOnlyRegisteredToolsApproved(t *testing.T) {
	box := newToolbox([]Tool{echoTool()})
	cfg := copilotSessionConfig(t.Context(), Config{}, box)
	if len(cfg.Tools) != 1 || len(cfg.AvailableTools) != 1 || *cfg.EnableConfigDiscovery {
		t.Fatal("unexpected tools or discovery")
	}
	allow := copilotPermission(box)
	decision, _ := allow(&rpc.PermissionRequestCustomTool{ToolName: "echo"}, copilot.PermissionInvocation{})
	if _, ok := decision.(*rpc.PermissionDecisionApproveOnce); !ok {
		t.Fatalf("registered tool denied: %T", decision)
	}
	decision, _ = allow(&rpc.PermissionRequestCustomTool{ToolName: "other"}, copilot.PermissionInvocation{})
	if _, ok := decision.(*rpc.PermissionDecisionDeniedByRules); !ok {
		t.Fatal("unknown tool approved")
	}
	required := true
	decision, _ = allow(&rpc.PermissionRequestCustomTool{ToolName: "echo", ManagedApprovalRequired: &required}, copilot.PermissionInvocation{})
	if _, ok := decision.(*rpc.PermissionDecisionDeniedByRules); !ok {
		t.Fatal("managed approval bypassed")
	}
	res, err := cfg.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"x": 1}})
	if err != nil || res.ResultType != "success" || res.TextResultForLLM != `got {"x":1}` {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCopilotToolHandlerPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tool := Tool{Name: "slow", Call: func(ctx context.Context, _ json.RawMessage) (string, error) { return "", ctx.Err() }}
	h := copilotTool(ctx, newToolbox([]Tool{tool}), tool).Handler
	if _, err := h(copilot.ToolInvocation{Arguments: map[string]any{}}); err == nil {
		t.Fatal("cancellation lost")
	}
}
