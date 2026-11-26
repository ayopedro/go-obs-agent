package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServer describes one remote (streamable HTTP) MCP server. Only tools named
// in AllowedTools are exposed to the model; there is no "allow all".
type MCPServer struct {
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"` // values may use ${ENV_VAR}
	AllowedTools []string          `json:"allowed_tools"`
}

type mcpConfig struct {
	Servers []MCPServer `json:"servers"`
}

var (
	serverName = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
	envRef     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

func LoadMCPConfig(path string) ([]MCPServer, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read MCP config: %w", err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse MCP config: %w", err)
	}
	seen := map[string]bool{}
	for _, s := range cfg.Servers {
		if !serverName.MatchString(s.Name) || seen[s.Name] {
			return nil, fmt.Errorf("MCP server name %q must be unique, lowercase letters, digits or underscores", s.Name)
		}
		seen[s.Name] = true
		if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
			return nil, fmt.Errorf("MCP server %q needs an http(s) url", s.Name)
		}
		if len(s.AllowedTools) == 0 {
			return nil, fmt.Errorf("MCP server %q must list allowed_tools", s.Name)
		}
	}
	return cfg.Servers, nil
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

func expandEnv(headers map[string]string, getenv func(string) string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		var missing string
		out[k] = envRef.ReplaceAllStringFunc(v, func(m string) string {
			val := getenv(envRef.FindStringSubmatch(m)[1])
			if val == "" {
				missing = m
			}
			return val
		})
		if missing != "" {
			return nil, fmt.Errorf("header %q references unset variable %s", k, missing)
		}
	}
	return out, nil
}

// ConnectMCP connects to each server and returns its allowed tools plus a closer.
// Tool names are prefixed mcp_<server>_ to avoid collisions.
func ConnectMCP(ctx context.Context, servers []MCPServer, getenv func(string) string) ([]Tool, func(), error) {
	var sessions []*mcp.ClientSession
	closeAll := func() {
		for _, s := range sessions {
			_ = s.Close()
		}
	}
	var tools []Tool
	for _, srv := range servers {
		headers, err := expandEnv(srv.Headers, getenv)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("MCP server %q: %w", srv.Name, err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "go-obs-agent", Version: "1.0.0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: headers}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("connect MCP server %q: %w", srv.Name, err)
		}
		sessions = append(sessions, session)
		allowed := map[string]bool{}
		for _, n := range srv.AllowedTools {
			allowed[n] = true
		}
		found := map[string]bool{}
		for t, err := range session.Tools(ctx, nil) {
			if err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("list tools for MCP server %q: %w", srv.Name, err)
			}
			if !allowed[t.Name] {
				continue
			}
			found[t.Name] = true
			tools = append(tools, mcpTool(session, srv.Name, t))
		}
		for n := range allowed {
			if !found[n] {
				closeAll()
				return nil, nil, fmt.Errorf("MCP server %q does not provide allowed tool %q", srv.Name, n)
			}
		}
	}
	return tools, closeAll, nil
}

func mcpTool(session *mcp.ClientSession, server string, t *mcp.Tool) Tool {
	name := "mcp_" + server + "_" + t.Name
	if len(name) > 64 {
		name = name[:64]
	}
	return Tool{Name: name, Description: t.Description, Schema: schemaMap(t.InputSchema), Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid input: %w", err)
			}
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: args})
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(text.Text)
				sb.WriteByte('\n')
			}
		}
		if res.IsError {
			return "", fmt.Errorf("%s", strings.TrimSpace(sb.String()))
		}
		return strings.TrimSpace(sb.String()), nil
	}}
}
