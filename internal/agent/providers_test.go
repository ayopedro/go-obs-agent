package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3/azure"
	openaiopt "github.com/openai/openai-go/v3/option"
)

// The adapters are exercised against fake HTTP APIs so request and response
// conversion is tested for real, not mocked at the SDK boundary.

func TestAzureProviderRoundTrip(t *testing.T) {
	var requests []map[string]any
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		requests = append(requests, m)
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("api-key") != "key" {
			t.Errorf("api-key header: %q", r.Header.Get("api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":1,"model":"gpt","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"x\":1}"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"2","object":"chat.completion","created":1,"model":"gpt","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"pool exhausted"}}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	}))
	defer ts.Close()

	p, err := newAzureProvider(Config{Provider: "azure", Model: "my-deployment", BaseURL: ts.URL, APIKey: "key"}, openaiopt.WithMaxRetries(0), azure.WithUnsafeAllowHTTP())
	if err != nil {
		t.Fatal(err)
	}
	out, err := newLoopEngine("my-deployment", p, newToolbox([]Tool{echoTool()}), nil).Investigate(t.Context(), "go")
	if err != nil || out != "pool exhausted" {
		t.Fatalf("%q %v", out, err)
	}
	if !strings.Contains(paths[0], "/openai/deployments/my-deployment/chat/completions") || !strings.Contains(paths[0], "api-version=") {
		t.Fatalf("not routed to the Azure deployment: %s", paths[0])
	}
	tools, _ := requests[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools not sent: %v", requests[0])
	}
	msgs, _ := requests[1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "c1" || last["content"] != `got {"x":1}` {
		t.Fatalf("tool result not returned to the model: %v", last)
	}
}

func TestAnthropicProviderRoundTrip(t *testing.T) {
	var requests []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		requests = append(requests, m)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"echo","input":{"x":1}}],"usage":{"input_tokens":5,"output_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"m2","type":"message","role":"assistant","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"pool exhausted"}],"usage":{"input_tokens":6,"output_tokens":3}}`))
	}))
	defer ts.Close()

	p := newAnthropicProvider(Config{Model: "m", APIKey: "k", BaseURL: ts.URL}, anthropicopt.WithMaxRetries(0))
	out, err := newLoopEngine("m", p, newToolbox([]Tool{echoTool()}), nil).Investigate(t.Context(), "go")
	if err != nil || out != "pool exhausted" {
		t.Fatalf("%q %v", out, err)
	}
	msgs, _ := requests[1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].([]any)
	block, _ := content[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "t1" {
		t.Fatalf("tool result not returned to the model: %v", last)
	}
}
