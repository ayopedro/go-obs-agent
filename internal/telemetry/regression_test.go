package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type clientFunc func(*http.Request) (*http.Response, error)

func (f clientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
func jsonClient(body string) HTTPClient {
	return clientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func TestTraceCorrelationAndStatusEncodings(t *testing.T) {
	for _, code := range []string{`2`, `"STATUS_CODE_ERROR"`} {
		t.Run(code, func(t *testing.T) {
			body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"spanId":"child","parentSpanId":"root","startTimeUnixNano":"1700000000010000000","endTimeUnixNano":"1700000000130000000","status":{"code":` + code + `}}]}]}]}`
			result, err := AnalyzeTraces(t.Context(), jsonClient(body), TraceInput{TraceID: "trace"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Errors) != 1 || result.Spans[0].ParentSpanID != "root" {
				t.Fatalf("lost error or hierarchy: %+v", result)
			}
			want := time.Unix(1700000000, 10000000).UTC()
			if result.StartTime == nil || !result.StartTime.Equal(want) || result.EndTime == nil || !result.Spans[0].EndTime.Equal(*result.EndTime) {
				t.Fatalf("lost incident time: %+v", result)
			}
		})
	}
}

func TestTelemetryPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := clientFunc(func(req *http.Request) (*http.Response, error) {
		if !errors.Is(req.Context().Err(), context.Canceled) {
			t.Error("request lost cancellation")
		}
		return nil, context.Canceled
	})
	_, traceErr := AnalyzeTraces(ctx, client, TraceInput{TraceID: "t"})
	_, metricErr := QueryMetrics(ctx, client, MetricsInput{Query: "up"})
	_, logErr := InspectLogs(ctx, client, LogsInput{Query: `{job="api"}`})
	for _, err := range []error{traceErr, metricErr, logErr} {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	}
}

func TestResponseSizeLimit(t *testing.T) {
	var target any
	if err := decodeResponse(strings.NewReader(strings.Repeat(" ", maxResponseBytes+1)), &target); err == nil {
		t.Fatal("oversized body accepted")
	}
}

func assertBudget(t *testing.T, result any, truncated bool) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(data) > maxOutputBytes {
		t.Fatalf("missing truncation or exceeded budget: %d bytes", len(data))
	}
}

func TestToolOutputBudgets(t *testing.T) {
	long := strings.Repeat("x", maxOutputBytes)
	t.Run("trace", func(t *testing.T) {
		body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"` + long + `","status":{"code":2}}]}]}]}`
		result, err := AnalyzeTraces(t.Context(), jsonClient(body), TraceInput{TraceID: "t"})
		if err != nil {
			t.Fatal(err)
		}
		assertBudget(t, result, result.Truncated)
		if result.SpanCount != 1 {
			t.Fatal("lost full span count")
		}
	})
	t.Run("metrics", func(t *testing.T) {
		body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"api"},"values":[[1700000000,"` + long + `"]]}]}}`
		result, err := QueryMetrics(t.Context(), jsonClient(body), MetricsInput{Query: "up"})
		if err != nil {
			t.Fatal(err)
		}
		assertBudget(t, result, result.Truncated)
	})
	t.Run("logs", func(t *testing.T) {
		client := clientFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("limit") != "100" {
				t.Fatal("log limit not clamped")
			}
			return jsonClient(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["1700000000000000000","` + long + `"]]}]}}`).Do(req)
		})
		result, err := InspectLogs(t.Context(), client, LogsInput{Query: `{job="api"}`, Limit: 100000})
		if err != nil {
			t.Fatal(err)
		}
		assertBudget(t, result, result.Truncated)
		if result.Total != len(result.Lines) {
			t.Fatal("total differs from returned lines")
		}
	})
}

func TestLogQueryLimitReportsPartialResults(t *testing.T) {
	result, err := InspectLogs(t.Context(), jsonClient(lokiResponseFixture(1, "success")), LogsInput{Query: `{job="api"}`, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Message != truncationMessage || result.Total != 1 {
		t.Fatalf("query limit was not reported: %+v", result)
	}
}
