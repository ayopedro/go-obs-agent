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

// MetricsInput defines the parameters accepted by the query_metrics tool.
type MetricsInput struct {
	// Query is a PromQL expression (required).
	Query string `json:"query" jsonschema:"PromQL expression to evaluate (e.g. rate(http_requests_total[5m]))"`
	// StartTime is the range start in RFC3339 format (optional; defaults to 15 minutes before EndTime).
	StartTime string `json:"start_time,omitempty" jsonschema:"Range start in RFC3339 format"`
	// EndTime is the range end in RFC3339 format (optional; defaults to now).
	EndTime string `json:"end_time,omitempty" jsonschema:"Range end in RFC3339 format"`
	// Step is the query resolution step (optional; defaults to 60s).
	Step string `json:"step,omitempty" jsonschema:"Resolution step duration (e.g. 30s, 1m). Defaults to 60s"`
}

// MetricSample is a single {timestamp, value} data point.
type MetricSample struct {
	Timestamp time.Time `json:"timestamp"`
	Value     string    `json:"value"`
}

// MetricSeries is a labelled time series of samples.
type MetricSeries struct {
	Labels  map[string]string `json:"labels"`
	Samples []MetricSample    `json:"samples"`
}

// MetricsResult is the structured output returned by query_metrics.
type MetricsResult struct {
	Query      string         `json:"query"`
	ResultType string         `json:"result_type"`
	Series     []MetricSeries `json:"series"`
	Message    string         `json:"message,omitempty"`
}

// prometheusRangeResponse mirrors the Prometheus range-query HTTP API response.
type prometheusRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"` // [[unixTimestamp, "value"], ...]
		} `json:"result"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// NewQueryMetricsTool constructs the query_metrics function tool.
// Pass a nil client to use the default http.Client.
func NewQueryMetricsTool(client HTTPClient) (tool.Tool, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return functiontool.New(
		functiontool.Config{
			Name:        "query_metrics",
			Description: "Execute a PromQL query against Prometheus and return time-series data. Use this to verify CPU/memory usage, error rates, or request throughput around an incident timestamp.",
		},
		func(_ tool.Context, input MetricsInput) (MetricsResult, error) {
			return runQueryMetrics(context.Background(), client, input)
		},
	)
}

func runQueryMetrics(ctx context.Context, client HTTPClient, input MetricsInput) (MetricsResult, error) {
	baseURL := os.Getenv("PROMETHEUS_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:9090"
	}

	if input.Query == "" {
		return MetricsResult{Message: "query is required"}, fmt.Errorf("query is required")
	}

	// Resolve time window.
	end := time.Now()
	if input.EndTime != "" {
		var err error
		end, err = time.Parse(time.RFC3339, input.EndTime)
		if err != nil {
			return MetricsResult{}, fmt.Errorf("invalid end_time %q: %w", input.EndTime, err)
		}
	}

	start := end.Add(-15 * time.Minute)
	if input.StartTime != "" {
		var err error
		start, err = time.Parse(time.RFC3339, input.StartTime)
		if err != nil {
			return MetricsResult{}, fmt.Errorf("invalid start_time %q: %w", input.StartTime, err)
		}
	}

	step := "60s"
	if input.Step != "" {
		step = input.Step
	}

	params := url.Values{}
	params.Set("query", input.Query)
	params.Set("start", fmt.Sprintf("%d", start.Unix()))
	params.Set("end", fmt.Sprintf("%d", end.Unix()))
	params.Set("step", step)

	endpoint := fmt.Sprintf("%s/api/v1/query_range?%s", baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return MetricsResult{}, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return MetricsResult{}, fmt.Errorf("prometheus request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return MetricsResult{}, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, string(body))
	}

	var payload prometheusRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return MetricsResult{}, fmt.Errorf("failed to decode prometheus response: %w", err)
	}

	if payload.Status != "success" {
		return MetricsResult{
			Query:   input.Query,
			Message: fmt.Sprintf("prometheus error [%s]: %s", payload.ErrorType, payload.Error),
		}, nil
	}

	series := make([]MetricSeries, 0, len(payload.Data.Result))
	for _, r := range payload.Data.Result {
		samples := make([]MetricSample, 0, len(r.Values))
		for _, v := range r.Values {
			if len(v) != 2 {
				continue
			}
			ts, ok := v[0].(float64)
			if !ok {
				continue
			}
			val, ok := v[1].(string)
			if !ok {
				val = fmt.Sprintf("%v", v[1])
			}
			samples = append(samples, MetricSample{
				Timestamp: time.Unix(int64(ts), 0).UTC(),
				Value:     val,
			})
		}
		series = append(series, MetricSeries{
			Labels:  r.Metric,
			Samples: samples,
		})
	}

	msg := ""
	if len(series) == 0 {
		msg = "no data returned for query"
	}

	return MetricsResult{
		Query:      input.Query,
		ResultType: payload.Data.ResultType,
		Series:     series,
		Message:    msg,
	}, nil
}
