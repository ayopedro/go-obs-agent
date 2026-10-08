// Package telemetry provides observability tool implementations for the agent.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

type TraceInput struct {
	// TraceID is the distributed trace identifier to look up (required).
	TraceID string `json:"trace_id" jsonschema:"The distributed trace ID to analyze"`
}

// SpanSummary is a condensed view of a single trace span.
type SpanSummary struct {
	ParentSpanID  string            `json:"parent_span_id,omitempty"`
	StartTime     *time.Time        `json:"start_time,omitempty"`
	EndTime       *time.Time        `json:"end_time,omitempty"`
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
	StartTime *time.Time    `json:"start_time,omitempty"`
	EndTime   *time.Time    `json:"end_time,omitempty"`
	Truncated bool          `json:"truncated"`
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
				SpanID       string `json:"spanId"`
				ParentSpanID string `json:"parentSpanId"`
				Name         string `json:"name"`
				StartTime    string `json:"startTimeUnixNano"`
				EndTime      string `json:"endTimeUnixNano"`
				Status       struct {
					Code    statusCode `json:"code"`
					Message string     `json:"message,omitempty"`
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

// AnalyzeTraces queries the backend with the supplied context and client.
// A nil client uses the default timeout.
func AnalyzeTraces(ctx context.Context, client HTTPClient, input TraceInput) (TraceResult, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := os.Getenv("TEMPO_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3200"
	}

	if len(input.TraceID) > 256 {
		return TraceResult{}, fmt.Errorf("trace_id exceeds 256-byte limit")
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return TraceResult{}, fmt.Errorf("tempo returned %d: %s", resp.StatusCode, string(body))
	}

	var payload tempoTrace
	if err := decodeResponse(resp.Body, &payload); err != nil {
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
	var startTime, endTime *time.Time

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
					if attr.Key == "http.status_code" || attr.Key == "http.response.status_code" {
						var code int
						if _, err := fmt.Sscanf(val, "%d", &code); err == nil && code >= 500 {
							isError = true
						}
					}
				}
				// Tempo uses STATUS_CODE_ERROR for error spans.
				if s.Status.Code == 2 {
					isError = true
				}

				durationMs := spanDurationMs(s.StartTime, s.EndTime)
				start, end := spanTime(s.StartTime), spanTime(s.EndTime)
				if start != nil && (startTime == nil || start.Before(*startTime)) {
					startTime = start
				}
				if end != nil && (endTime == nil || end.After(*endTime)) {
					endTime = end
				}

				summary := SpanSummary{
					SpanID:        s.SpanID,
					ParentSpanID:  s.ParentSpanID,
					StartTime:     start,
					EndTime:       end,
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

	result := TraceResult{
		StartTime: startTime,
		EndTime:   endTime,
		TraceID:   input.TraceID,
		SpanCount: len(spans),
		Spans:     spans,
		Errors:    errSpans,
	}
	err = boundOutput(&result, &result.Truncated, &result.Message, func() bool {
		if len(result.Spans) == 0 {
			return false
		}
		result.Spans = result.Spans[:len(result.Spans)/2]
		result.Errors = nil
		for _, span := range result.Spans {
			if span.Error {
				result.Errors = append(result.Errors, span)
			}
		}
		return true
	})
	return result, err
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

// statusCode accepts both OTLP numeric enums and protobuf enum names.
type statusCode int

func (c *statusCode) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*c = statusCode(n)
		return nil
	}
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	switch name {
	case "STATUS_CODE_UNSET":
		*c = 0
	case "STATUS_CODE_OK":
		*c = 1
	case "STATUS_CODE_ERROR":
		*c = 2
	default:
		return fmt.Errorf("unknown span status %q", name)
	}
	return nil
}
func spanTime(value string) *time.Time {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	t := time.Unix(0, n).UTC()
	return &t
}
