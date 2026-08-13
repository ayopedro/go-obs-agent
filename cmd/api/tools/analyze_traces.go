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
	Tags          map[string]string `json:"tags,omitempty"`
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

// jaegerTrace mirrors the subset of the Jaeger HTTP API response we need.
type jaegerTrace struct {
	Data []struct {
		TraceID string `json:"traceID"`
		Spans   []struct {
			SpanID        string `json:"spanID"`
			OperationName string `json:"operationName"`
			Duration      int64  `json:"duration"` // microseconds
			Tags          []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"tags"`
			Process struct {
				ServiceName string `json:"serviceName"`
			} `json:"process"`
		} `json:"spans"`
	} `json:"data"`
	Total  int    `json:"total"`
	Errors []any  `json:"errors"`
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
			Description: "Fetch and analyze a distributed trace by ID from Jaeger. Returns span-level latency, error flags, and service names to help isolate failing components.",
		},
		func(_ tool.Context, input TraceInput) (TraceResult, error) {
			return runAnalyzeTraces(context.Background(), client, input)
		},
	)
}

func runAnalyzeTraces(ctx context.Context, client HTTPClient, input TraceInput) (TraceResult, error) {
	baseURL := os.Getenv("JAEGER_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:16686"
	}

	if input.TraceID == "" {
		return TraceResult{Message: "trace_id is required"}, fmt.Errorf("trace_id is required")
	}

	endpoint := fmt.Sprintf("%s/api/traces/%s", baseURL, url.PathEscape(input.TraceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TraceResult{}, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return TraceResult{}, fmt.Errorf("jaeger request failed: %w", err)
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
		return TraceResult{}, fmt.Errorf("jaeger returned %d: %s", resp.StatusCode, string(body))
	}

	var payload jaegerTrace
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return TraceResult{}, fmt.Errorf("failed to decode jaeger response: %w", err)
	}

	if len(payload.Data) == 0 {
		return TraceResult{
			TraceID: input.TraceID,
			Message: "no trace data returned",
		}, nil
	}

	trace := payload.Data[0]
	spans := make([]SpanSummary, 0, len(trace.Spans))
	var errors []SpanSummary

	for _, s := range trace.Spans {
		tags := make(map[string]string)
		isError := false
		for _, t := range s.Tags {
			key := t.Key
			val := fmt.Sprintf("%v", t.Value)
			tags[key] = val
			if key == "error" && (val == "true" || val == "1") {
				isError = true
			}
			if key == "http.status_code" {
				if val >= "500" {
					isError = true
				}
			}
		}

		summary := SpanSummary{
			SpanID:        s.SpanID,
			OperationName: s.OperationName,
			ServiceName:   s.Process.ServiceName,
			DurationMs:    float64(s.Duration) / 1000.0,
			Error:         isError,
			Tags:          tags,
		}
		spans = append(spans, summary)
		if isError {
			errors = append(errors, summary)
		}
	}

	return TraceResult{
		TraceID:   input.TraceID,
		SpanCount: len(spans),
		Spans:     spans,
		Errors:    errors,
	}, nil
}
