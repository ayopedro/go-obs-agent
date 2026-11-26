package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/ayopedro/go-obs-agent/internal/audit"
	"github.com/ayopedro/go-obs-agent/internal/redact"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

// copilotEngine lets the Copilot runtime own the loop; our tools run through the
// shared toolbox so they are allowlisted and audited like the other providers.
type copilotEngine struct {
	client  *copilot.Client
	session *copilot.Session
	close   func()
}

func startCopilot(ctx context.Context, cfg Config, box *toolbox, closer func()) (Engine, error) {
	path := cfg.CLIPath
	if path == "" {
		var err error
		path, err = exec.LookPath("copilot")
		if err != nil {
			return nil, fmt.Errorf("Copilot CLI not found: install it or set COPILOT_CLI_PATH: %w", err)
		}
	}
	client := copilot.NewClient(&copilot.ClientOptions{Connection: copilot.StdioConnection{Path: path}, GitHubToken: cfg.GitHubToken, LogLevel: "error"})
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start Copilot runtime: %w", err)
	}
	session, err := client.CreateSession(ctx, copilotSessionConfig(ctx, cfg, box))
	if err != nil {
		_ = client.Stop()
		return nil, fmt.Errorf("create observability session: %w", err)
	}
	return &copilotEngine{client: client, session: session, close: closer}, nil
}

func copilotSessionConfig(ctx context.Context, cfg Config, box *toolbox) *copilot.SessionConfig {
	set := copilot.NewToolSet()
	tools := make([]copilot.Tool, 0, len(box.order))
	for _, t := range box.order {
		set.AddCustom(t.Name)
		tools = append(tools, copilotTool(ctx, box, t))
	}
	return &copilot.SessionConfig{
		Model: cfg.Model, Tools: tools, AvailableTools: set.ToSlice(),
		ClientName: "go-obs-agent", EnableConfigDiscovery: copilot.Bool(false),
		SystemMessage:       &copilot.SystemMessageConfig{Mode: "replace", Content: instructions},
		OnPermissionRequest: copilotPermission(box),
	}
}

func copilotTool(ctx context.Context, box *toolbox, t Tool) copilot.Tool {
	return copilot.Tool{Name: t.Name, Description: t.Description, Parameters: t.Schema, Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
		callCtx, cancel := invocationContext(ctx, inv)
		defer cancel()
		args, err := json.Marshal(inv.Arguments)
		if err != nil {
			return copilot.ToolResult{}, err
		}
		content, isErr := box.run(callCtx, t.Name, args)
		if callCtx.Err() != nil {
			return copilot.ToolResult{}, callCtx.Err()
		}
		if isErr {
			return copilot.ToolResult{TextResultForLLM: content, ResultType: "failure", Error: content}, nil
		}
		return copilot.ToolResult{TextResultForLLM: content, ResultType: "success"}, nil
	}}
}

// Only registered tools are approved; anything else, or anything needing managed
// approval, is denied.
func copilotPermission(box *toolbox) copilot.PermissionHandlerFunc {
	return func(req copilot.PermissionRequest, _ copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
		if req.RequiresManagedApproval() {
			return &rpc.PermissionDecisionDeniedByRules{}, nil
		}
		if custom, ok := req.(*rpc.PermissionRequestCustomTool); ok {
			if _, registered := box.tools[custom.ToolName]; registered {
				return &rpc.PermissionDecisionApproveOnce{}, nil
			}
		}
		return &rpc.PermissionDecisionDeniedByRules{}, nil
	}
}

// Honor both application shutdown and the runtime's per-call cancellation.
func invocationContext(ctx context.Context, inv copilot.ToolInvocation) (context.Context, context.CancelFunc) {
	parent := inv.TraceContext
	if parent == nil {
		parent = ctx
	}
	parent = audit.WithActor(parent, audit.ActorFrom(ctx))
	child, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	return child, func() { stop(); cancel() }
}

func (e *copilotEngine) Investigate(ctx context.Context, prompt string) (string, error) {
	turnCtx, cancel := context.WithTimeout(ctx, investigationTO)
	defer cancel()
	started := time.Now()
	prompt, _ = redact.Text(prompt)
	audit.Log(ctx, "agent.started", append(audit.Prompt(prompt), "provider", "copilot")...)
	finish := func(outcome string) {
		audit.Log(ctx, "agent.completed", "outcome", outcome, "duration_ms", time.Since(started).Milliseconds())
	}
	event, err := e.session.SendAndWait(turnCtx, copilot.MessageOptions{Prompt: prompt})
	if err != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer abortCancel()
		_ = e.session.Abort(abortCtx)
		finish("model_error")
		return "", err
	}
	if event == nil {
		finish("no_response")
		return "", fmt.Errorf("agent completed without a response")
	}
	message, ok := event.Data.(*copilot.AssistantMessageData)
	if !ok {
		finish("no_response")
		return "", fmt.Errorf("unexpected agent response")
	}
	finish("answered")
	return message.Content, nil
}

func (e *copilotEngine) Close() error {
	_ = e.session.Disconnect()
	err := e.client.Stop()
	if e.close != nil {
		e.close()
	}
	return err
}
