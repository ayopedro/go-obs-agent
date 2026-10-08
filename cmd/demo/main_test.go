package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ayopedro/go-obs-agent/internal/telemetry"
)

func TestDemoIncidentThroughTelemetryFunctions(t *testing.T) {
	incident := time.Unix(1700000000, 0).UTC()
	server := httptest.NewServer(demoHandler(incident))
	defer server.Close()
	for _, key := range []string{"TEMPO_BASE_URL", "PROMETHEUS_BASE_URL", "LOKI_BASE_URL"} {
		t.Setenv(key, server.URL)
	}
	trace, err := telemetry.AnalyzeTraces(t.Context(), server.Client(), telemetry.TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatal(err)
	}
	if trace.SpanCount != 2 || len(trace.Errors) != 2 || trace.StartTime == nil || !trace.StartTime.Equal(incident) || trace.Spans[1].ParentSpanID != trace.Spans[0].SpanID {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	for _, name := range []string{"db_pool_active_connections", "db_pool_max_connections"} {
		result, err := telemetry.QueryMetrics(t.Context(), server.Client(), telemetry.MetricsInput{Query: name})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Series) != 1 || result.Series[0].Samples[0].Value != "100" {
			t.Fatalf("unexpected pool metric: %+v", result)
		}
	}
	logs, err := telemetry.InspectLogs(t.Context(), server.Client(), telemetry.LogsInput{Query: `{service="checkout"}`})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 1 {
		t.Fatalf("missing incident log: %+v", logs)
	}
	missing, err := telemetry.AnalyzeTraces(t.Context(), server.Client(), telemetry.TraceInput{TraceID: "missing"})
	if err != nil || missing.Message == "" {
		t.Fatalf("missing trace not reported: %+v %v", missing, err)
	}
}
