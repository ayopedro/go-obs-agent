package agent

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

const defaultAzureAPIVersion = "2024-10-21"

type azureProvider struct {
	client     openai.Client
	deployment string
}

// newAzureProvider uses an API key when set, otherwise Microsoft Entra ID
// (managed identity, workload identity, or az login) via DefaultAzureCredential.
func newAzureProvider(cfg Config, extra ...option.RequestOption) (*azureProvider, error) {
	version := cfg.AzureAPIVersion
	if version == "" {
		version = defaultAzureAPIVersion
	}
	opts := []option.RequestOption{azure.WithEndpoint(cfg.BaseURL, version)}
	if cfg.APIKey != "" {
		opts = append(opts, azure.WithAPIKey(cfg.APIKey))
	} else {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("Azure credential: %w", err)
		}
		opts = append(opts, azure.WithTokenCredential(cred))
	}
	return &azureProvider{client: openai.NewClient(append(opts, extra...)...), deployment: cfg.Model}, nil
}

func (p *azureProvider) NewSession(system string, tools []Tool) Session {
	params := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := map[string]any{"type": "object"}
		for k, v := range t.Schema {
			schema[k] = v
		}
		params = append(params, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: t.Name, Description: openai.String(t.Description), Parameters: shared.FunctionParameters(schema),
		}))
	}
	return &azureSession{p: p, tools: params, messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system)}}
}

type azureSession struct {
	p        *azureProvider
	tools    []openai.ChatCompletionToolUnionParam
	messages []openai.ChatCompletionMessageParamUnion
}

func (s *azureSession) Send(ctx context.Context, in Input) (Reply, error) {
	if in.Prompt != "" {
		s.messages = append(s.messages, openai.UserMessage(in.Prompt))
	}
	for _, r := range in.Results {
		s.messages = append(s.messages, openai.ToolMessage(r.Content, r.CallID))
	}
	resp, err := s.p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: s.p.deployment, Messages: s.messages, Tools: s.tools,
		MaxCompletionTokens: openai.Int(maxTokens),
	})
	if err != nil {
		return Reply{}, err
	}
	if len(resp.Choices) == 0 {
		return Reply{}, fmt.Errorf("Azure OpenAI returned no choices")
	}
	choice := resp.Choices[0]
	s.messages = append(s.messages, choice.Message.ToParam())
	reply := Reply{Text: choice.Message.Content, InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens, Truncated: choice.FinishReason == "length"}
	for _, call := range choice.Message.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Input: []byte(call.Function.Arguments)})
	}
	return reply, nil
}
