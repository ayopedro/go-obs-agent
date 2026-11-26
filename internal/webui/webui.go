// Package webui serves a single-page UI for asking the agent questions.
// It is meant for local testing: it has no login and only answers requests
// addressed to a loopback host name.
package webui

import (
	"context"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ayopedro/go-obs-agent/internal/audit"
)

//go:embed index.html
var page []byte

const (
	maxPromptBytes = 8 << 10
	maxConcurrent  = 2
	timeout        = 6 * time.Minute
)

type Investigator interface {
	Investigate(context.Context, string) (string, error)
}

// Register adds the UI routes to mux.
func Register(mux *http.ServeMux, engine Investigator) {
	slots := make(chan struct{}, maxConcurrent)
	mux.Handle("GET /{$}", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		_, _ = w.Write(page)
	})))
	mux.Handle("POST /api/investigate", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Prompt string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPromptBytes)).Decode(&body); err != nil || body.Prompt == "" {
			fail(w, http.StatusBadRequest, "send a JSON body with a non-empty prompt")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			fail(w, http.StatusTooManyRequests, "too many investigations running; try again shortly")
			return
		}
		// The server-wide write timeout is short; investigations take minutes.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 10*time.Second))
		ctx, cancel := context.WithTimeout(audit.WithActor(r.Context(), audit.Actor{Platform: "web", User: "local"}), timeout)
		defer cancel()
		audit.Log(ctx, "request.accepted", audit.Prompt(body.Prompt)...)
		answer, err := engine.Investigate(ctx, body.Prompt)
		if err != nil {
			audit.Warn(ctx, "investigation.failed", "error", err.Error())
			fail(w, http.StatusBadGateway, "investigation failed; check the server logs and configuration")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"answer": answer})
	})))
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// local rejects requests whose Host or Origin is not loopback. This blocks
// DNS-rebinding and cross-site requests; it is not a substitute for a login.
func local(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.Host) {
			http.Error(w, "web UI is only available on localhost", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !loopback(u.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
