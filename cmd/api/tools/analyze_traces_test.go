package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tempoFixture builds a minimal Grafana Tempo OTLP JSON response.
func tempoFixture(traceID string, errorStatus string, httpStatusCode string) []byte {
	spans := []map[string]any{
		{
			"spanId":             "span1",
			"name":               "HTTP GET /api/users",
			"startTimeUnixNano":  "1700000000000000000",
			"endTimeUnixNano":    "1700000000005000000",
			"status":             map[string]any{"code": "STATUS_CODE_OK"},
			"attributes":         []map[string]any{{"key": "http.status_code", "value": map[string]any{"stringValue": "200"}}},
		},
		{
			"spanId":            "span2",
			"name":              "db.query",
			"startTimeUnixNano": "1700000000010000000",
			"endTimeUnixNano":   "1700000000130000000",
			"status":            map[string]any{"code": errorStatus},
			"attributes": []map[string]any{
				{"key": "http.status_code", "value": map[string]any{"stringValue": httpStatusCode}},
			},
		},
	}

	payload := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": "api-server"}},
					},
				},
				"scopeSpans": []map[string]any{
					{"spans": spans},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func TestRunAnalyzeTraces_HappyPath(t *testing.T) {
	traceID := "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, traceID) {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept: application/json, got %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tempoFixture(traceID, "STATUS_CODE_OK", "200"))
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

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
		t.Errorf("expected 0 error spans, got %d", len(result.Errors))
	}
	if result.Spans[0].ServiceName != "api-server" {
		t.Errorf("expected service name 'api-server', got %q", result.Spans[0].ServiceName)
	}
}

func TestRunAnalyzeTraces_WithStatusCodeError(t *testing.T) {
	traceID := "error-trace"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tempoFixture(traceID, "STATUS_CODE_ERROR", "500"))
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

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

func TestRunAnalyzeTraces_DurationCalculated(t *testing.T) {
	traceID := "dur-trace"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"resourceSpans": []map[string]any{
				{
					"resource": map[string]any{
						"attributes": []map[string]any{
							{"key": "service.name", "value": map[string]any{"stringValue": "svc"}},
						},
					},
					"scopeSpans": []map[string]any{
						{
							"spans": []map[string]any{
								{
									"spanId":            "s1",
									"name":              "op",
									"startTimeUnixNano": "1000000000", // 1s in ns
									"endTimeUnixNano":   "2000000000", // 2s in ns → 1000ms
									"status":            map[string]any{"code": "STATUS_CODE_OK"},
									"attributes":        []map[string]any{},
								},
							},
						},
					},
				},
			},
		}
		b, _ := json.Marshal(payload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Spans[0].DurationMs != 1000.0 {
		t.Errorf("expected 1000ms duration, got %f", result.Spans[0].DurationMs)
	}
}

func TestRunAnalyzeTraces_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "missing"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Message, "not found") {
		t.Errorf("expected 'not found' message, got %q", result.Message)
	}
}

func TestRunAnalyzeTraces_EmptyResourceSpans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resourceSpans":[]}`))
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

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

	t.Setenv("TEMPO_BASE_URL", srv.URL)

	_, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: "t1"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "tempo returned 500") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRunAnalyzeTraces_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

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
	if !strings.Contains(tl.Description(), "Tempo") {
		t.Errorf("description should mention Tempo, got: %q", tl.Description())
	}
}

func TestRunAnalyzeTraces_HTTP500AttributeMarksError(t *testing.T) {
	traceID := "http500"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// span with http.status_code=503 but STATUS_CODE_OK — attribute alone should flag error
		payload := map[string]any{
			"resourceSpans": []map[string]any{
				{
					"resource": map[string]any{
						"attributes": []map[string]any{
							{"key": "service.name", "value": map[string]any{"stringValue": "checkout"}},
						},
					},
					"scopeSpans": []map[string]any{
						{
							"spans": []map[string]any{
								{
									"spanId":            "s1",
									"name":              "POST /checkout",
									"startTimeUnixNano": "0",
									"endTimeUnixNano":   "0",
									"status":            map[string]any{"code": "STATUS_CODE_OK"},
									"attributes": []map[string]any{
										{"key": "http.status_code", "value": map[string]any{"stringValue": "503"}},
									},
								},
							},
						},
					},
				},
			},
		}
		b, _ := json.Marshal(payload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	t.Setenv("TEMPO_BASE_URL", srv.URL)

	result, err := runAnalyzeTraces(t.Context(), srv.Client(), TraceInput{TraceID: traceID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected span with http.status_code=503 to be counted as an error")
	}
}
