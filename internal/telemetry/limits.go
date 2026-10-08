package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
)

const (
	maxResponseBytes  = 4 << 20
	maxOutputBytes    = 64 << 10
	maxLogLines       = 100
	truncationMessage = "results are partial: output budget or query limit reached; narrow the query before drawing conclusions"
)

// Bound backend bodies before decoding, including bodies with unknown fields.
func decodeResponse(body io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d-byte limit; narrow the query", maxResponseBytes)
	}
	return json.Unmarshal(data, target)
}

// Measure serialized bytes so long attributes, labels and log lines also count.
func boundOutput(result any, truncated *bool, message *string, shrink func() bool) error {
	for {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if len(data) <= maxOutputBytes {
			return nil
		}
		*truncated = true
		*message = truncationMessage
		if !shrink() {
			return fmt.Errorf("result metadata exceeds output budget; narrow the query")
		}
	}
}
