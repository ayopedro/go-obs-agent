package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
