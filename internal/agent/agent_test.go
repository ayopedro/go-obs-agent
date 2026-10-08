package agent

import (
	"context"
	"errors"
	"net/http"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

func configFrom(values map[string]string) (Config, error) {
	return LoadConfig(func(k string) string { return values[k] })
}
func TestProviderConfiguration(t *testing.T) {
	c, err := configFrom(nil)
	if err != nil || c.Provider != "copilot" || c.providerConfig() != nil {
		t.Fatalf("bad default: %+v, %v", c, err)
	}
	for _, provider := range []string{"openai", "azure", "anthropic"} {
		c, err := configFrom(map[string]string{"LLM_PROVIDER": provider, "LLM_MODEL": "approved-model", "LLM_BASE_URL": "https://gateway.example/v1/", "LLM_API_KEY": "secret"})
		if err != nil {
			t.Fatal(err)
		}
		p := c.providerConfig()
		if p.Type != provider || p.BaseURL != "https://gateway.example/v1" || p.APIKey != "secret" {
			t.Fatalf("bad provider mapping: %+v", p)
		}
	}
	for _, v := range []map[string]string{
		{"LLM_PROVIDER": "unsupported"},
		{"LLM_PROVIDER": "openai"},
		{"LLM_API_KEY": "secret"},
		{"LLM_PROVIDER": "openai", "LLM_MODEL": "m", "LLM_BASE_URL": "file:///tmp/model"},
		{"LLM_PROVIDER": "openai", "LLM_MODEL": "m", "LLM_BASE_URL": "https://secret@gateway.example"},
		{"LLM_PROVIDER": "openai", "LLM_MODEL": "m", "LLM_BASE_URL": "https://gateway.example", "LLM_WIRE_API": "invalid"},
	} {
		if _, err := configFrom(v); err == nil {
			t.Fatalf("accepted invalid configuration: %v", v)
		}
	}
}

func TestOnlyTelemetryToolsExposed(t *testing.T) {
	cfg := sessionConfig(t.Context(), Config{Provider: "copilot"})
	if len(cfg.Tools) != 3 || len(cfg.AvailableTools) != 3 || *cfg.EnableConfigDiscovery {
		t.Fatal("unexpected tools or discovery")
	}
	for i, name := range []string{"analyze_traces", "query_metrics", "inspect_logs"} {
		if cfg.Tools[i].Name != name || cfg.AvailableTools[i] != "custom:"+name {
			t.Fatalf("unexpected tool filter: %v", cfg.AvailableTools)
		}
		req := &rpc.PermissionRequestCustomTool{ToolName: name}
		decision, err := cfg.OnPermissionRequest(req, copilot.PermissionInvocation{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := decision.(*rpc.PermissionDecisionApproveOnce); !ok {
			t.Fatalf("telemetry denied: %T", decision)
		}
	}
	decision, _ := telemetryPermission(&rpc.PermissionRequestCustomTool{ToolName: "other"}, copilot.PermissionInvocation{})
	if _, ok := decision.(*rpc.PermissionDecisionDeniedByRules); !ok {
		t.Fatal("unknown tool approved")
	}
	required := true
	decision, _ = telemetryPermission(&rpc.PermissionRequestCustomTool{ToolName: "inspect_logs", ManagedApprovalRequired: &required}, copilot.PermissionInvocation{})
	if _, ok := decision.(*rpc.PermissionDecisionDeniedByRules); !ok {
		t.Fatal("managed approval bypassed")
	}
}

type cancelClient struct{ t *testing.T }

func (c cancelClient) Do(req *http.Request) (*http.Response, error) {
	if !errors.Is(req.Context().Err(), context.Canceled) {
		c.t.Fatal("callback lost cancellation")
	}
	return nil, context.Canceled
}
func TestToolAdaptersPropagateCancellation(t *testing.T) {
	for _, cancelInvocation := range []bool{false, true} {
		app, cancelApp := context.WithCancel(t.Context())
		defer cancelApp()
		invocation, cancelInv := context.WithCancel(t.Context())
		defer cancelInv()
		if cancelInvocation {
			cancelInv()
		} else {
			cancelApp()
		}
		for _, tl := range Tools(app, cancelClient{t}) {
			args := map[string]any{"query": "up"}
			if tl.Name == "analyze_traces" {
				args = map[string]any{"trace_id": "trace"}
			}
			_, err := tl.Handler(copilot.ToolInvocation{Arguments: args, TraceContext: invocation})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s lost cancellation: %v", tl.Name, err)
			}
		}
	}
}
