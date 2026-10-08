// Demo serves synthetic API fixtures, not real Tempo, Prometheus or Loki.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const traceID = "0123456789abcdef0123456789abcdef"

func demoHandler(incident time.Time) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/traces/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != traceID {
			http.NotFound(w, r)
			return
		}
		span := func(id, parent, name string, start time.Time, duration time.Duration, code int, attrs []map[string]any) map[string]any {
			return map[string]any{"spanId": id, "parentSpanId": parent, "name": name, "startTimeUnixNano": fmt.Sprint(start.UnixNano()), "endTimeUnixNano": fmt.Sprint(start.Add(duration).UnixNano()), "status": map[string]any{"code": code}, "attributes": attrs}
		}
		write(w, map[string]any{"resourceSpans": []any{map[string]any{
			"resource": map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "checkout"}}}},
			"scopeSpans": []any{map[string]any{"spans": []any{
				span("0000000000000001", "", "POST /checkout", incident, 2*time.Second, 2, nil),
				span("0000000000000002", "0000000000000001", "db.acquire_connection", incident.Add(time.Millisecond), 1900*time.Millisecond, 2, []map[string]any{{"key": "error.message", "value": map[string]any{"stringValue": "database connection pool exhausted"}}}),
			}}},
		}}})
	})
	mux.HandleFunc("GET /api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		// Only known fixture metrics are returned; unrelated queries yield no data.
		var results []any
		for _, metric := range []struct{ name, value string }{{"db_pool_active_connections", "100"}, {"db_pool_max_connections", "100"}, {"process_cpu_usage", "0.12"}} {
			if r.URL.Query().Get("query") == metric.name {
				results = append(results, map[string]any{"metric": map[string]string{"__name__": metric.name, "service": "checkout"}, "values": [][]any{{float64(incident.Unix()), metric.value}}})
			}
		}
		if results == nil {
			results = []any{}
		}
		write(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": results}})
	})
	mux.HandleFunc("GET /loki/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"service": "checkout"}, "values": [][]string{{fmt.Sprint(incident.UnixNano()), "trace_id=" + traceID + " ERROR database connection pool exhausted; timeout waiting for a connection"}}}}}})
	})
	return mux
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	incident := time.Now().UTC().Add(-2 * time.Minute)
	server := &http.Server{Addr: "127.0.0.1:4319", Handler: demoHandler(incident), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Synthetic telemetry on http://127.0.0.1:4319\nTrace ID: %s\nIncident: %s\nMetrics: db_pool_active_connections, db_pool_max_connections, process_cpu_usage\n", traceID, incident.Format(time.RFC3339))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
