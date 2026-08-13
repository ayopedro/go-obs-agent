// Package tools provides observability tool implementations for the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// TraceInput defines the parameters accepted by the analyze_traces tool.
type TraceInput struct {
	// TraceID is the distributed trace identifier to look up (required).
	TraceID string `json:"trace_id" jsonschema:"The distributed trace ID to analyze"`
	// StartTime is the window start in RFC3339 format (optional; defaults to 15 minutes before EndTime).
	StartTime string `json:"start_time,omitempty" jsonschema:"Window start time in RFC3339 format (e.g. 2006-01-02T15:04:05Z)"`
	// EndTime is the window end in RFC3339 format (optional; defaults to now).
	EndTime string `json:"end_time,omitempty" jsonschema:"Window end time in RFC3339 format (e.g. 2006-01-02T15:04:05Z)"`
}

// SpanSummary is a condensed view of a single trace span.
type SpanSummary struct {
	SpanID        string            `json:"span_id"`
	OperationName string            `json:"operation_name"`
	ServiceName   string            `json:"service_name"`
	DurationMs    float64           `json:"duration_ms"`
	Error         bool              `json:"error"`
	Attributes    map[string]string `json:"attributes,omitempty"`
}

// TraceResult is the structured output returned by analyze_traces.
type TraceResult struct {
	TraceID   string        `json:"trace_id"`
	SpanCount int           `json:"span_count"`
	Spans     []SpanSummary `json:"spans"`
	Errors    []SpanSummary `json:"errors"`
	Message   string        `json:"message,omitempty"`
}

// HTTPClient abstracts HTTP calls so the tool can be unit-tested without a real server.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// tempoTrace mirrors the subset of the Grafana Tempo HTTP API response we need.
// Tempo returns OTLP JSON: /api/traces/{traceID} → { "resourceSpans": [...] }
type tempoTrace struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				SpanID    string     `json:"spanId"`
				Name      string     `json:"name"`
				StartTime string     `json:"startTimeUnixNano"`
				EndTime   string     `json:"endTimeUnixNano"`
				Status    struct {
					Code    string `json:"code"`
					Message string `json:"message,omitempty"`
				} `json:"status"`
				Attributes []otlpAttr `json:"attributes"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// otlpAttr is a single OTLP key/value attribute.
type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue,omitempty"`
		IntValue    string `json:"intValue,omitempty"`
		BoolValue   bool   `json:"boolValue,omitempty"`
	} `json:"value"`
}

func (a otlpAttr) stringVal() string {
	if a.Value.StringValue != "" {
		return a.Value.StringValue
	}
	if a.Value.IntValue != "" {
		return a.Value.IntValue
	}
	if a.Value.BoolValue {
		return "true"
	}
	return ""
}

// NewAnalyzeTracesTool constructs the analyze_traces function tool.
// Pass a nil client to use the default http.Client.
func NewAnalyzeTracesTool(client HTTPClient) (tool.Tool, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return functiontool.New(
		functiontool.Config{
			Name:        "analyze_traces",
			Description: "Fetch and analyze a distributed trace by ID from Grafana Tempo. Returns span-level latency, error flags, and service names to help isolate failing components.",
		},
		func(_ tool.Context, input TraceInput) (TraceResult, error) {
			return runAnalyzeTraces(context.Background(), client, input)
		},
	)
}

func runAnalyzeTraces(ctx context.Context, client HTTPClient, input TraceInput) (TraceResult, error) {
	baseURL := os.Getenv("TEMPO_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3200"
	}

	if input.TraceID == "" {
		return TraceResult{Message: "trace_id is required"}, fmt.Errorf("trace_id is required")
	}

	endpoint := fmt.Sprintf("%s/api/traces/%s", baseURL, url.PathEscape(input.TraceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TraceResult{}, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return TraceResult{}, fmt.Errorf("tempo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return TraceResult{
			TraceID: input.TraceID,
			Message: fmt.Sprintf("trace %s not found", input.TraceID),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return TraceResult{}, fmt.Errorf("tempo returned %d: %s", resp.StatusCode, string(body))
	}

	var payload tempoTrace
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return TraceResult{}, fmt.Errorf("failed to decode tempo response: %w", err)
	}

	if len(payload.ResourceSpans) == 0 {
		return TraceResult{
			TraceID: input.TraceID,
			Message: "no trace data returned",
		}, nil
	}

	var spans []SpanSummary
	var errSpans []SpanSummary

	for _, rs := range payload.ResourceSpans {
		// Extract service name from resource attributes.
		serviceName := ""
		for _, attr := range rs.Resource.Attributes {
			if attr.Key == "service.name" {
				serviceName = attr.stringVal()
				break
			}
		}

		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				attrs := make(map[string]string, len(s.Attributes))
				isError := false
				for _, attr := range s.Attributes {
					val := attr.stringVal()
					attrs[attr.Key] = val
					if attr.Key == "error" && val == "true" {
						isError = true
					}
					if attr.Key == "http.status_code" && val >= "500" {
						isError = true
					}
				}
				// Tempo uses STATUS_CODE_ERROR for error spans.
				if s.Status.Code == "STATUS_CODE_ERROR" {
					isError = true
				}

				durationMs := spanDurationMs(s.StartTime, s.EndTime)

				summary := SpanSummary{
					SpanID:        s.SpanID,
					OperationName: s.Name,
					ServiceName:   serviceName,
					DurationMs:    durationMs,
					Error:         isError,
					Attributes:    attrs,
				}
				spans = append(spans, summary)
				if isError {
					errSpans = append(errSpans, summary)
				}
			}
		}
	}

	return TraceResult{
		TraceID:   input.TraceID,
		SpanCount: len(spans),
		Spans:     spans,
		Errors:    errSpans,
	}, nil
}

// spanDurationMs computes duration in milliseconds from OTLP nanosecond timestamps.
func spanDurationMs(startNano, endNano string) float64 {
	var start, end int64
	fmt.Sscanf(startNano, "%d", &start)
	fmt.Sscanf(endNano, "%d", &end)
	if start == 0 || end == 0 || end < start {
		return 0
	}
	return float64(end-start) / 1e6
}
