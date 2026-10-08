package agent

import (
	"fmt"
	"net/url"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
)

// Config chooses inference independently of telemetry collection.
type Config struct {
	Provider        string
	Model           string
	BaseURL         string
	APIKey          string
	WireAPI         string
	AzureAPIVersion string
	CLIPath         string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{Provider: getenv("LLM_PROVIDER"), Model: getenv("LLM_MODEL"), BaseURL: getenv("LLM_BASE_URL"), APIKey: getenv("LLM_API_KEY"), WireAPI: getenv("LLM_WIRE_API"), AzureAPIVersion: getenv("LLM_AZURE_API_VERSION"), CLIPath: getenv("COPILOT_CLI_PATH")}
	if c.Provider == "" {
		c.Provider = "copilot"
	}
	switch c.Provider {
	case "copilot":
		if c.BaseURL != "" || c.APIKey != "" || c.WireAPI != "" || c.AzureAPIVersion != "" {
			return Config{}, fmt.Errorf("custom endpoint settings require LLM_PROVIDER=openai, azure, or anthropic")
		}
	case "openai", "azure", "anthropic":
		if c.Model == "" || c.BaseURL == "" {
			return Config{}, fmt.Errorf("custom providers require LLM_MODEL and LLM_BASE_URL")
		}
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("LLM_BASE_URL must be an HTTP(S) endpoint without embedded credentials, query, or fragment")
		}
		if c.WireAPI != "" && c.WireAPI != "completions" && c.WireAPI != "responses" {
			return Config{}, fmt.Errorf("LLM_WIRE_API must be completions or responses")
		}
		if c.Provider == "anthropic" && c.WireAPI != "" {
			return Config{}, fmt.Errorf("LLM_WIRE_API only applies to openai and azure")
		}
		if c.AzureAPIVersion != "" && c.Provider != "azure" {
			return Config{}, fmt.Errorf("LLM_AZURE_API_VERSION only applies to azure")
		}
	default:
		return Config{}, fmt.Errorf("unsupported LLM_PROVIDER %q", c.Provider)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c, nil
}

func (c Config) providerConfig() *copilot.ProviderConfig {
	if c.Provider == "copilot" {
		return nil
	}
	p := &copilot.ProviderConfig{Type: c.Provider, BaseURL: c.BaseURL, APIKey: c.APIKey, WireAPI: c.WireAPI}
	if c.AzureAPIVersion != "" {
		p.Azure = &copilot.AzureProviderOptions{APIVersion: c.AzureAPIVersion}
	}
	return p
}
