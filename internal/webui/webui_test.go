package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct{ err error }

func (f fake) Investigate(_ context.Context, p string) (string, error) {
	return "answer to " + p, f.err
}

func do(h http.Handler, method, path, host, origin, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUI(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, fake{})
	if w := do(mux, "GET", "/", "localhost:8080", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "<textarea") {
		t.Fatalf("page: %d", w.Code)
	}
	w := do(mux, "POST", "/api/investigate", "127.0.0.1:8080", "http://127.0.0.1:8080", `{"prompt":"x"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "answer to x") {
		t.Fatalf("investigate: %d %s", w.Code, w.Body)
	}
	for name, c := range map[string]*httptest.ResponseRecorder{
		"remote host":  do(mux, "POST", "/api/investigate", "example.com", "", `{"prompt":"x"}`),
		"rebinding":    do(mux, "GET", "/", "evil.example:8080", "", ""),
		"cross origin": do(mux, "POST", "/api/investigate", "localhost:8080", "https://evil.example", `{"prompt":"x"}`),
	} {
		if c.Code != http.StatusForbidden {
			t.Fatalf("%s: got %d", name, c.Code)
		}
	}
	if w := do(mux, "POST", "/api/investigate", "localhost", "", `{"prompt":""}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: %d", w.Code)
	}
}

func TestInvestigationErrorIsGeneric(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, fake{err: errors.New("secret detail")})
	w := do(mux, "POST", "/api/investigate", "localhost", "", `{"prompt":"x"}`)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "secret detail") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
