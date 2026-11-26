package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ayopedro/go-obs-agent/internal/telemetry"
	"github.com/google/jsonschema-go/jsonschema"
)

// Tool is a model-callable function. Telemetry and MCP tools share this shape,
// so adding a tool never changes the agent loop.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema object for the input
	Call        func(ctx context.Context, input json.RawMessage) (string, error)
}

func typedTool[In, Out any](name, description string, fn func(context.Context, In) (Out, error)) Tool {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("schema for %s: %v", name, err))
	}
	return Tool{Name: name, Description: description, Schema: schemaMap(schema), Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
		var in In
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
		out, err := fn(ctx, in)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(out)
		return string(b), err
	}}
}

func schemaMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// TelemetryTools wraps the telemetry functions. A nil client uses their default.
func TelemetryTools(client telemetry.HTTPClient) []Tool {
	return []Tool{
		typedTool("analyze_traces", "Fetch a Tempo trace by ID, including service names, parent spans, error flags and incident timestamps.", func(ctx context.Context, in telemetry.TraceInput) (telemetry.TraceResult, error) {
			return telemetry.AnalyzeTraces(ctx, client, in)
		}),
		typedTool("query_metrics", "Query Prometheus using PromQL over an explicit incident time window.", func(ctx context.Context, in telemetry.MetricsInput) (telemetry.MetricsResult, error) {
			return telemetry.QueryMetrics(ctx, client, in)
		}),
		typedTool("inspect_logs", "Query Loki using LogQL over an explicit incident time window, optionally filtering by trace ID.", func(ctx context.Context, in telemetry.LogsInput) (telemetry.LogsResult, error) {
			return telemetry.InspectLogs(ctx, client, in)
		}),
	}
}
