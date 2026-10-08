package agent

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

func sessionConfig(ctx context.Context, c Config) *copilot.SessionConfig {
	allowed := copilot.NewToolSet().AddCustom("analyze_traces").AddCustom("query_metrics").AddCustom("inspect_logs").ToSlice()
	return &copilot.SessionConfig{
		Model: c.Model, Provider: c.providerConfig(), Tools: Tools(ctx, nil), AvailableTools: allowed,
		ClientName: "go-obs-agent", EnableConfigDiscovery: copilot.Bool(false),
		SystemMessage:       &copilot.SystemMessageConfig{Mode: "replace", Content: instructions},
		OnPermissionRequest: telemetryPermission,
	}
}

func telemetryPermission(req copilot.PermissionRequest, _ copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
	if req.RequiresManagedApproval() {
		return &rpc.PermissionDecisionDeniedByRules{}, nil
	}
	if custom, ok := req.(*rpc.PermissionRequestCustomTool); ok {
		switch custom.ToolName {
		case "analyze_traces", "query_metrics", "inspect_logs":
			return &rpc.PermissionDecisionApproveOnce{}, nil
		}
	}
	return &rpc.PermissionDecisionDeniedByRules{}, nil
}

// Runtime owns orchestration; telemetry functions have no dependency on it.
type Runtime struct {
	client  *copilot.Client
	session *copilot.Session
}

func Start(ctx context.Context, cfg Config) (*Runtime, error) {
	path := cfg.CLIPath
	if path == "" {
		var err error
		path, err = exec.LookPath("copilot")
		if err != nil {
			return nil, fmt.Errorf("Copilot CLI not found: install it or set COPILOT_CLI_PATH: %w", err)
		}
	}
	client := copilot.NewClient(&copilot.ClientOptions{Connection: copilot.StdioConnection{Path: path}, LogLevel: "error"})
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start Copilot runtime: %w", err)
	}
	session, err := client.CreateSession(ctx, sessionConfig(ctx, cfg))
	if err != nil {
		_ = client.Stop()
		return nil, fmt.Errorf("create observability session: %w", err)
	}
	return &Runtime{client: client, session: session}, nil
}

func (r *Runtime) Investigate(ctx context.Context, prompt string) (string, error) {
	turnCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	event, err := r.session.SendAndWait(turnCtx, copilot.MessageOptions{Prompt: prompt})
	if err != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer abortCancel()
		_ = r.session.Abort(abortCtx)
		return "", err
	}
	if event == nil {
		return "", fmt.Errorf("agent completed without a response")
	}
	message, ok := event.Data.(*copilot.AssistantMessageData)
	if !ok {
		return "", fmt.Errorf("unexpected agent response")
	}
	return message.Content, nil
}
func (r *Runtime) Close() error { _ = r.session.Disconnect(); return r.client.Stop() }
