package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jaegerTraceFixture builds a minimal Jaeger JSON payload for a given traceID.
func jaegerTraceFixture(traceID string, includeErrorSpan bool) string {
	errorTag := `{"key":"error","value":"false"}`
	if includeErrorSpan {
		errorTag = `{"key":"error","value":"true"}`
	}
	return `{
		"data": [{
			"traceID": "` + traceID + `",
			"spans": [
				{
					"spanID": "span1",
					"operationName": "HTTP GET /api/users",
					"duration": 5000,
					"tags": [{"key":"http.status_code","value":"200"}],
					"process": {"serviceName": "frontend"}
				},
				{
					"spanID": "span2",
					"operationName": "db.query",
					"duration": 120000,
					"tags": [` + errorTag + `],
					"process": {"serviceName": "db-service"}
				}
			]
		}],
		"total": 1,
		"errors": null
	}`
}

func TestRunAnalyzeTraces_HappyPath(t *testing.T) {
	traceID := "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, traceID) {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jaegerTraceFixture(traceID, false)))
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TraceID != traceID {
		t.Errorf("expected TraceID %q, got %q", traceID, result.TraceID)
	}
	if result.SpanCount != 2 {
		t.Errorf("expected 2 spans, got %d", result.SpanCount)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
}

func TestRunAnalyzeTraces_WithErrors(t *testing.T) {
	traceID := "error-trace"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jaegerTraceFixture(traceID, true)))
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected at least one error span")
	}
	if !result.Errors[0].Error {
		t.Error("error span should have Error=true")
	}
}

func TestRunAnalyzeTraces_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "missing"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Message, "not found") {
		t.Errorf("expected 'not found' message, got %q", result.Message)
	}
}

func TestRunAnalyzeTraces_EmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"total":0,"errors":null}`))
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "any"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Message, "no trace data") {
		t.Errorf("expected 'no trace data' message, got %q", result.Message)
	}
}

func TestRunAnalyzeTraces_MissingTraceID(t *testing.T) {
	_, err := runAnalyzeTraces(t.Context(), http.DefaultClient, TraceInput{})
	if err == nil {
		t.Fatal("expected error for empty trace_id")
	}
}

func TestRunAnalyzeTraces_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	_, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "t1"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestRunAnalyzeTraces_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	_, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "t1"})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNewAnalyzeTracesTool_ReturnsValidTool(t *testing.T) {
	tl, err := NewAnalyzeTracesTool(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tl.Name() != "analyze_traces" {
		t.Errorf("unexpected tool name: %s", tl.Name())
	}
	if tl.Description() == "" {
		t.Error("description should not be empty")
	}
}

func TestRunAnalyzeTraces_HTTP500TagMarksError(t *testing.T) {
	traceID := "http500"
	fixture, _ := json.Marshal(map[string]any{
		"data": []map[string]any{
			{
				"traceID": traceID,
				"spans": []map[string]any{
					{
						"spanID":        "s1",
						"operationName": "POST /checkout",
						"duration":      3000,
						"tags":          []map[string]any{{"key": "http.status_code", "value": "503"}},
						"process":       map[string]any{"serviceName": "checkout-svc"},
					},
				},
			},
		},
		"total":  1,
		"errors": nil,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	t.Setenv("JAEGER_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected span with http.status_code=503 to be counted as an error")
	}
}
