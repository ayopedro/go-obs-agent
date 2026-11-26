package agent

import (
	"context"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type anthropicProvider struct {
	client anthropic.Client
	model  string
}

func newAnthropicProvider(cfg Config, extra ...option.RequestOption) *anthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(append(opts, extra...)...), model: cfg.Model}
}

func (p *anthropicProvider) NewSession(system string, tools []Tool) Session {
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{Properties: t.Schema["properties"]}
		if req, ok := t.Schema["required"].([]any); ok {
			for _, v := range req {
				if s, ok := v.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
		params = append(params, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema}})
	}
	return &anthropicSession{p: p, system: system, tools: params}
}

type anthropicSession struct {
	p        *anthropicProvider
	system   string
	tools    []anthropic.ToolUnionParam
	messages []anthropic.MessageParam
}

func (s *anthropicSession) Send(ctx context.Context, in Input) (Reply, error) {
	if in.Prompt != "" {
		s.messages = append(s.messages, anthropic.NewUserMessage(anthropic.NewTextBlock(in.Prompt)))
	}
	if len(in.Results) > 0 {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(in.Results))
		for _, r := range in.Results {
			blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		s.messages = append(s.messages, anthropic.NewUserMessage(blocks...))
	}
	resp, err := s.p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model(s.p.model), MaxTokens: maxTokens,
		System:   []anthropic.TextBlockParam{{Text: s.system}},
		Messages: s.messages, Tools: s.tools,
	})
	if err != nil {
		return Reply{}, err
	}
	s.messages = append(s.messages, resp.ToParam())
	reply := Reply{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, Truncated: resp.StopReason == anthropic.StopReasonMaxTokens}
	var text strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.AsText().Text)
		case "tool_use":
			use := block.AsToolUse()
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: use.ID, Name: use.Name, Input: use.Input})
		}
	}
	reply.Text = text.String()
	return reply, nil
}
