package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// LogsInput defines the parameters accepted by the inspect_logs tool.
type LogsInput struct {
	// Query is a LogQL stream selector and filter expression (required).
	// Example: `{service="api-server"} |= "error"`
	Query string `json:"query" jsonschema:"LogQL query (e.g. {service=api-server} |= error)"`
	// StartTime is the window start in RFC3339 format (optional; defaults to 15 minutes before EndTime).
	StartTime string `json:"start_time,omitempty" jsonschema:"Window start time in RFC3339 format"`
	// EndTime is the window end in RFC3339 format (optional; defaults to now).
	EndTime string `json:"end_time,omitempty" jsonschema:"Window end time in RFC3339 format"`
	// Limit is the maximum number of log lines to return (optional; defaults to 100).
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of log lines to return (default 100)"`
}

// LogLine represents a single log entry.
type LogLine struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// LogsResult is the structured output returned by inspect_logs.
type LogsResult struct {
	Truncated bool      `json:"truncated"`
	Query     string    `json:"query"`
	Lines     []LogLine `json:"lines"`
	Total     int       `json:"total"`
	Message   string    `json:"message,omitempty"`
}

// lokiQueryResponse mirrors the Loki query_range HTTP API response.
type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [["unixNano", "logline"], ...]
		} `json:"result"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// InspectLogs queries the backend with the supplied context and client.
// A nil client uses the default timeout.
func InspectLogs(ctx context.Context, client HTTPClient, input LogsInput) (LogsResult, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := os.Getenv("LOKI_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3100"
	}

	if len(input.Query) > 4096 {
		return LogsResult{}, fmt.Errorf("query exceeds 4096-byte limit; narrow the query")
	}
	if input.Query == "" {
		return LogsResult{Message: "query is required"}, fmt.Errorf("query is required")
	}

	// Resolve time window.
	end := time.Now()
	if input.EndTime != "" {
		var err error
		end, err = time.Parse(time.RFC3339, input.EndTime)
		if err != nil {
			return LogsResult{}, fmt.Errorf("invalid end_time %q: %w", input.EndTime, err)
		}
	}

	start := end.Add(-15 * time.Minute)
	if input.StartTime != "" {
		var err error
		start, err = time.Parse(time.RFC3339, input.StartTime)
		if err != nil {
			return LogsResult{}, fmt.Errorf("invalid start_time %q: %w", input.StartTime, err)
		}
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}

	if limit > maxLogLines {
		limit = maxLogLines
	}
	params := url.Values{}
	params.Set("query", input.Query)
	params.Set("start", fmt.Sprintf("%d", start.UnixNano()))
	params.Set("end", fmt.Sprintf("%d", end.UnixNano()))
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("direction", "forward")

	endpoint := fmt.Sprintf("%s/loki/api/v1/query_range?%s", baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return LogsResult{}, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return LogsResult{}, fmt.Errorf("loki request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return LogsResult{}, fmt.Errorf("loki returned %d: %s", resp.StatusCode, string(body))
	}

	var payload lokiQueryResponse
	if err := decodeResponse(resp.Body, &payload); err != nil {
		return LogsResult{}, fmt.Errorf("failed to decode loki response: %w", err)
	}

	if payload.Status != "success" {
		return LogsResult{
			Query:   input.Query,
			Message: fmt.Sprintf("loki error [%.256s]: %.4096s", payload.ErrorType, payload.Error),
		}, nil
	}

	var lines []LogLine
	for _, stream := range payload.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}
			var ts time.Time
			var nsec int64
			if _, err := fmt.Sscanf(entry[0], "%d", &nsec); err == nil {
				ts = time.Unix(0, nsec).UTC()
			}
			lines = append(lines, LogLine{
				Timestamp: ts,
				Line:      entry[1],
				Labels:    stream.Stream,
			})
		}
	}

	msg := ""
	if len(lines) == 0 {
		msg = "no log lines matched the query"
	}

	result := LogsResult{
		Truncated: len(lines) >= limit,
		Query:     input.Query,
		Lines:     lines,
		Total:     len(lines),
		Message:   msg,
	}
	if result.Truncated {
		result.Message = truncationMessage
	}
	err = boundOutput(&result, &result.Truncated, &result.Message, func() bool {
		if len(result.Lines) == 0 {
			return false
		}
		result.Lines = result.Lines[:len(result.Lines)/2]
		result.Total = len(result.Lines)
		return true
	})
	return result, err
}
