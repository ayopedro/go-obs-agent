package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLoadConfig(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := LoadConfig(get(map[string]string{"LLM_MODEL": "m", "LLM_API_KEY": "k", "LLM_BASE_URL": "https://gw.example/"}))
	if err != nil || c.BaseURL != "https://gw.example" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, m := range []map[string]string{
		{},
		{"LLM_MODEL": "m"},
		{"LLM_MODEL": "m", "LLM_API_KEY": "k", "LLM_PROVIDER": "openai"},
		{"LLM_MODEL": "m", "LLM_API_KEY": "k", "LLM_BASE_URL": "https://u:p@gw.example"},
	} {
		if _, err := LoadConfig(get(m)); err == nil {
			t.Fatalf("accepted %v", m)
		}
	}
}

type scripted struct {
	replies []string // raw Message JSON
	calls   []anthropic.MessageNewParams
}

func (s *scripted) New(_ context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
	s.calls = append(s.calls, p)
	var m anthropic.Message
	if err := json.Unmarshal([]byte(s.replies[len(s.calls)-1]), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

const toolUse = `{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"%s","input":{"x":1}}]}`

func reply(name string) string { return strings.Replace(toolUse, "%s", name, 1) }

const final = `{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"root cause: pool exhausted"}]}`

func TestInvestigateRunsToolsAndReturnsAnswer(t *testing.T) {
	llm := &scripted{replies: []string{reply("echo"), reply("missing"), final}}
	echo := Tool{Name: "echo", Schema: map[string]any{"type": "object"}, Call: func(_ context.Context, in json.RawMessage) (string, error) { return "got " + string(in), nil }}
	r := newRuntime("m", llm, []Tool{echo}, nil)
	out, err := r.Investigate(t.Context(), "go")
	if err != nil || out != "root cause: pool exhausted" {
		t.Fatalf("%q %v", out, err)
	}
	if len(llm.calls) != 3 {
		t.Fatalf("calls: %d", len(llm.calls))
	}
	results := func(i int) anthropic.ToolResultBlockParam {
		last := llm.calls[i].Messages[len(llm.calls[i].Messages)-1]
		return *last.Content[0].OfToolResult
	}
	if r0 := results(1); r0.IsError.Value || r0.Content[0].OfText.Text != `got {"x":1}` {
		t.Fatalf("tool result: %+v", r0)
	}
	if r1 := results(2); !r1.IsError.Value || !strings.Contains(r1.Content[0].OfText.Text, "unknown tool") {
		t.Fatalf("unknown tool not reported as error: %+v", r1)
	}
}

func TestInvestigateStopsAfterMaxTurns(t *testing.T) {
	llm := &scripted{}
	for range maxTurns {
		llm.replies = append(llm.replies, reply("echo"))
	}
	echo := Tool{Name: "echo", Schema: map[string]any{}, Call: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	if _, err := newRuntime("m", llm, []Tool{echo}, nil).Investigate(t.Context(), "go"); err == nil {
		t.Fatal("loop did not stop")
	}
}

func TestTelemetryToolSchemas(t *testing.T) {
	tools := newRuntime("m", nil, TelemetryTools(nil), nil).toolParams()
	if len(tools) != 3 {
		t.Fatalf("tools: %d", len(tools))
	}
	if req := tools[0].OfTool.InputSchema.Required; len(req) != 1 || req[0] != "trace_id" {
		t.Fatalf("required: %v", req)
	}
}

func TestMCPToolsAreAllowlisted(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type in struct {
		Name string `json:"name"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "greets"}, func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi " + a.Name}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dangerous", Description: "restarts"}, func(context.Context, *mcp.CallToolRequest, in) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	var auth string
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	get := func(k string) string { return map[string]string{"TOKEN": "abc"}[k] }
	srv := MCPServer{Name: "ops", URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}, AllowedTools: []string{"greet"}}
	tools, closeAll, err := ConnectMCP(t.Context(), []MCPServer{srv}, get)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if len(tools) != 1 || tools[0].Name != "mcp_ops_greet" {
		t.Fatalf("allowlist not enforced: %+v", tools)
	}
	out, err := tools[0].Call(t.Context(), json.RawMessage(`{"name":"bob"}`))
	if err != nil || out != "hi bob" {
		t.Fatalf("%q %v", out, err)
	}
	if auth != "Bearer abc" {
		t.Fatalf("header not sent: %q", auth)
	}

	srv.AllowedTools = []string{"nope"}
	if _, _, err := ConnectMCP(t.Context(), []MCPServer{srv}, get); err == nil {
		t.Fatal("missing allowed tool accepted")
	}
	srv.AllowedTools = []string{"greet"}
	if _, _, err := ConnectMCP(t.Context(), []MCPServer{srv}, func(string) string { return "" }); err == nil {
		t.Fatal("unset header variable accepted")
	}
}

func TestLoadMCPConfigValidation(t *testing.T) {
	write := func(s string) string {
		p := filepath.Join(t.TempDir(), "mcp.json")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if s, err := LoadMCPConfig(write(`{"servers":[{"name":"a","url":"https://x","allowed_tools":["t"]}]}`)); err != nil || len(s) != 1 {
		t.Fatalf("%v %v", s, err)
	}
	for _, bad := range []string{
		`{"servers":[{"name":"a","url":"https://x"}]}`,
		`{"servers":[{"name":"A b","url":"https://x","allowed_tools":["t"]}]}`,
		`{"servers":[{"name":"a","url":"ftp://x","allowed_tools":["t"]}]}`,
	} {
		if _, err := LoadMCPConfig(write(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
