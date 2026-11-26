package agent

import (
	"fmt"
	"net/url"
	"strings"
)

// Config selects the Anthropic model and optional MCP tool servers.
type Config struct {
	Model         string
	BaseURL       string // empty uses the Anthropic API default
	APIKey        string
	MCPConfigPath string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if p := getenv("LLM_PROVIDER"); p != "" && p != "anthropic" {
		return Config{}, fmt.Errorf("unsupported LLM_PROVIDER %q: only anthropic is supported", p)
	}
	c := Config{Model: getenv("LLM_MODEL"), BaseURL: getenv("LLM_BASE_URL"), APIKey: getenv("LLM_API_KEY"), MCPConfigPath: getenv("MCP_CONFIG")}
	if c.Model == "" || c.APIKey == "" {
		return Config{}, fmt.Errorf("LLM_MODEL and LLM_API_KEY are required")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("LLM_BASE_URL must be an HTTP(S) endpoint without embedded credentials, query, or fragment")
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	}
	return c, nil
}
