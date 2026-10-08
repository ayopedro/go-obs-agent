package telemetry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func lokiResponseFixture(numLines int, status string) string {
	values := ""
	for i := 0; i < numLines; i++ {
		if i > 0 {
			values += ","
		}
		ns := time.Now().Add(time.Duration(i) * time.Second).UnixNano()
		values += fmt.Sprintf(`["%d", "log line %d"]`, ns, i+1)
	}
	return fmt.Sprintf(`{
		"status": "%s",
		"data": {
			"resultType": "streams",
			"result": [
				{
					"stream": {"service": "api-server", "env": "prod"},
					"values": [%s]
				}
			]
		}
	}`, status, values)
}

func TestRunInspectLogs_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(lokiResponseFixture(3, "success")))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	result, err := InspectLogs(t.Context(), srv.Client(), LogsInput{
		Query: `{service="api-server"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 3 {
		t.Errorf("expected 3 lines, got %d", result.Total)
	}
	if len(result.Lines) != 3 {
		t.Errorf("expected 3 entries in Lines, got %d", len(result.Lines))
	}
	if result.Lines[0].Labels["service"] != "api-server" {
		t.Errorf("expected label service=api-server, got %v", result.Lines[0].Labels)
	}
}

func TestRunInspectLogs_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	result, err := InspectLogs(t.Context(), srv.Client(), LogsInput{
		Query: `{service="ghost"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected 0 lines, got %d", result.Total)
	}
	if !strings.Contains(result.Message, "no log lines") {
		t.Errorf("expected 'no log lines' message, got %q", result.Message)
	}
}

func TestRunInspectLogs_MissingQuery(t *testing.T) {
	_, err := InspectLogs(t.Context(), http.DefaultClient, LogsInput{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRunInspectLogs_LokiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "error",
			"errorType": "bad_data",
			"error": "parse error at line 1"
		}`))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	result, err := InspectLogs(t.Context(), srv.Client(), LogsInput{Query: `{bad query`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Message, "parse error") {
		t.Errorf("expected loki error in message, got %q", result.Message)
	}
}

func TestRunInspectLogs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	_, err := InspectLogs(t.Context(), srv.Client(), LogsInput{Query: `{service="api"}`})
	if err == nil {
		t.Fatal("expected error for 502 response")
	}
}

func TestRunInspectLogs_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	_, err := InspectLogs(t.Context(), srv.Client(), LogsInput{Query: `{service="api"}`})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRunInspectLogs_InvalidStartTime(t *testing.T) {
	_, err := InspectLogs(t.Context(), http.DefaultClient, LogsInput{
		Query:     `{service="api"}`,
		StartTime: "bad-time",
	})
	if err == nil {
		t.Fatal("expected error for invalid start_time")
	}
}

func TestRunInspectLogs_InvalidEndTime(t *testing.T) {
	_, err := InspectLogs(t.Context(), http.DefaultClient, LogsInput{
		Query:   `{service="api"}`,
		EndTime: "bad-time",
	})
	if err == nil {
		t.Fatal("expected error for invalid end_time")
	}
}

func TestRunInspectLogs_DefaultLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("expected default limit=100, got %q", r.URL.Query().Get("limit"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(lokiResponseFixture(0, "success")))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	_, err := InspectLogs(t.Context(), srv.Client(), LogsInput{
		Query: `{service="api"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunInspectLogs_CustomLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "25" {
			t.Errorf("expected limit=25, got %q", r.URL.Query().Get("limit"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(lokiResponseFixture(0, "success")))
	}))
	defer srv.Close()

	t.Setenv("LOKI_BASE_URL", srv.URL)

	_, err := InspectLogs(t.Context(), srv.Client(), LogsInput{
		Query: `{service="api"}`,
		Limit: 25,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
