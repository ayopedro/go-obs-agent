package agent

import (
	"fmt"
	"net/url"
	"strings"
)

// Config selects the model provider and optional MCP tool servers.
type Config struct {
	Provider        string // azure, copilot, or anthropic
	Model           string // azure: deployment name
	BaseURL         string // azure: resource endpoint; anthropic: optional gateway
	APIKey          string // azure: optional (Entra ID when empty); anthropic: required
	AzureAPIVersion string
	CLIPath         string // copilot
	GitHubToken     string // copilot: non-interactive auth
	MCPConfigPath   string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		Provider: getenv("LLM_PROVIDER"), Model: getenv("LLM_MODEL"), BaseURL: getenv("LLM_BASE_URL"), APIKey: getenv("LLM_API_KEY"),
		AzureAPIVersion: getenv("LLM_AZURE_API_VERSION"), CLIPath: getenv("COPILOT_CLI_PATH"), GitHubToken: getenv("COPILOT_GITHUB_TOKEN"),
		MCPConfigPath: getenv("MCP_CONFIG"),
	}
	switch c.Provider {
	case "copilot":
		if c.BaseURL != "" || c.APIKey != "" || c.AzureAPIVersion != "" {
			return Config{}, fmt.Errorf("LLM_BASE_URL, LLM_API_KEY and LLM_AZURE_API_VERSION do not apply to LLM_PROVIDER=copilot")
		}
	case "azure":
		if c.Model == "" || c.BaseURL == "" {
			return Config{}, fmt.Errorf("LLM_PROVIDER=azure requires LLM_MODEL (deployment name) and LLM_BASE_URL (resource endpoint)")
		}
		if err := validateURL(c.BaseURL, true); err != nil {
			return Config{}, err
		}
	case "anthropic":
		if c.Model == "" || c.APIKey == "" {
			return Config{}, fmt.Errorf("LLM_PROVIDER=anthropic requires LLM_MODEL and LLM_API_KEY")
		}
		if c.BaseURL != "" {
			if err := validateURL(c.BaseURL, false); err != nil {
				return Config{}, err
			}
		}
		if c.AzureAPIVersion != "" {
			return Config{}, fmt.Errorf("LLM_AZURE_API_VERSION only applies to azure")
		}
	case "":
		return Config{}, fmt.Errorf("set LLM_PROVIDER to azure, copilot, or anthropic")
	default:
		return Config{}, fmt.Errorf("unsupported LLM_PROVIDER %q: use azure, copilot, or anthropic", c.Provider)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c, nil
}

func validateURL(raw string, httpsOnly bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && (httpsOnly || u.Scheme != "http")) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("LLM_BASE_URL must be an HTTP(S) endpoint without embedded credentials, query, or fragment (HTTPS required for azure)")
	}
	return nil
}
