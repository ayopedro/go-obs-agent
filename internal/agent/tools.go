package agent

import (
	"context"
	"github.com/ayopedro/go-obs-agent/internal/telemetry"
	copilot "github.com/github/copilot-sdk/go"
)

// Tools is the only SDK-specific layer around the telemetry functions.
func Tools(ctx context.Context, client telemetry.HTTPClient) []copilot.Tool {
	return []copilot.Tool{
		copilot.DefineTool("analyze_traces", "Fetch a Tempo trace by ID, including service names, parent spans, error flags and incident timestamps.", func(input telemetry.TraceInput, inv copilot.ToolInvocation) (telemetry.TraceResult, error) {
			callCtx, cancel := invocationContext(ctx, inv)
			defer cancel()
			return telemetry.AnalyzeTraces(callCtx, client, input)
		}),
		copilot.DefineTool("query_metrics", "Query Prometheus using PromQL over an explicit incident time window.", func(input telemetry.MetricsInput, inv copilot.ToolInvocation) (telemetry.MetricsResult, error) {
			callCtx, cancel := invocationContext(ctx, inv)
			defer cancel()
			return telemetry.QueryMetrics(callCtx, client, input)
		}),
		copilot.DefineTool("inspect_logs", "Query Loki using LogQL over an explicit incident time window, optionally filtering by trace ID.", func(input telemetry.LogsInput, inv copilot.ToolInvocation) (telemetry.LogsResult, error) {
			callCtx, cancel := invocationContext(ctx, inv)
			defer cancel()
			return telemetry.InspectLogs(callCtx, client, input)
		}),
	}
}

// Honor both application shutdown and the runtime's per-call cancellation.
func invocationContext(ctx context.Context, inv copilot.ToolInvocation) (context.Context, context.CancelFunc) {
	parent := inv.TraceContext
	if parent == nil {
		parent = ctx
	}
	child, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	return child, func() { stop(); cancel() }
}
