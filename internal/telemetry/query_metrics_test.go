package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunQueryMetrics_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "matrix",
				"result": [
					{
						"metric": {"job": "api", "__name__": "http_requests_total"},
						"values": [[1700000000, "42"], [1700000060, "55"]]
					}
				]
			}
		}`))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	result, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{
		Query: `http_requests_total`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ResultType != "matrix" {
		t.Errorf("expected resultType 'matrix', got %q", result.ResultType)
	}
	if len(result.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(result.Series))
	}
	if len(result.Series[0].Samples) != 2 {
		t.Errorf("expected 2 samples, got %d", len(result.Series[0].Samples))
	}
	if result.Series[0].Samples[0].Value != "42" {
		t.Errorf("expected first sample value '42', got %q", result.Series[0].Samples[0].Value)
	}
}

func TestRunQueryMetrics_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	result, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: `absent_metric`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Series) != 0 {
		t.Errorf("expected 0 series, got %d", len(result.Series))
	}
	if !strings.Contains(result.Message, "no data") {
		t.Errorf("expected 'no data' message, got %q", result.Message)
	}
}

func TestRunQueryMetrics_MissingQuery(t *testing.T) {
	_, err := QueryMetrics(t.Context(), http.DefaultClient, MetricsInput{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRunQueryMetrics_PrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "error",
			"errorType": "bad_data",
			"error": "invalid expression"
		}`))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	result, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: `bad query!!`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Message, "invalid expression") {
		t.Errorf("expected error message to contain 'invalid expression', got %q", result.Message)
	}
}

func TestRunQueryMetrics_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	_, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: `up`})
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}

func TestRunQueryMetrics_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	_, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: `up`})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRunQueryMetrics_InvalidStartTime(t *testing.T) {
	_, err := QueryMetrics(t.Context(), http.DefaultClient, MetricsInput{
		Query:     `up`,
		StartTime: "not-a-time",
	})
	if err == nil {
		t.Fatal("expected error for invalid start_time")
	}
}

func TestRunQueryMetrics_InvalidEndTime(t *testing.T) {
	_, err := QueryMetrics(t.Context(), http.DefaultClient, MetricsInput{
		Query:   `up`,
		EndTime: "not-a-time",
	})
	if err == nil {
		t.Fatal("expected error for invalid end_time")
	}
}

func TestRunQueryMetrics_CustomTimeWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("start") == "" || q.Get("end") == "" {
			t.Error("expected start and end query params")
		}
		if q.Get("step") != "30s" {
			t.Errorf("expected step=30s, got %q", q.Get("step"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	_, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{
		Query:     `up`,
		StartTime: "2024-01-01T00:00:00Z",
		EndTime:   "2024-01-01T01:00:00Z",
		Step:      "30s",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
