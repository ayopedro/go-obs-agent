package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuthPerBackend(t *testing.T) {
	var gotUser, gotPass string
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotAuth = r.BasicAuth()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	t.Setenv("PROMETHEUS_BASE_URL", srv.URL)

	if _, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: "up"}); err != nil || gotAuth {
		t.Fatalf("no credentials configured: err=%v auth=%v", err, gotAuth)
	}

	// Credentials for another backend must not leak into this one.
	t.Setenv("LOKI_USERNAME", "other")
	t.Setenv("LOKI_PASSWORD", "other")
	t.Setenv("PROMETHEUS_USERNAME", "12345")
	t.Setenv("PROMETHEUS_PASSWORD", "token")
	if _, err := QueryMetrics(t.Context(), srv.Client(), MetricsInput{Query: "up"}); err != nil {
		t.Fatal(err)
	}
	if !gotAuth || gotUser != "12345" || gotPass != "token" {
		t.Fatalf("got %q/%q auth=%v", gotUser, gotPass, gotAuth)
	}
}

func TestAuthRefusesUnsafeConfig(t *testing.T) {
	req := func(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }
	t.Setenv("TEMPO_USERNAME", "u")
	t.Setenv("TEMPO_PASSWORD", "p")
	if err := setAuth(req("http://tempo.example.com/x"), "TEMPO"); err == nil {
		t.Fatal("credentials sent over plain HTTP to a remote host")
	}
	for _, ok := range []string{"https://tempo.example.com/x", "http://localhost:3200/x", "http://127.0.0.1:3200/x"} {
		if err := setAuth(req(ok), "TEMPO"); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	t.Setenv("TEMPO_PASSWORD", "")
	if err := setAuth(req("https://tempo.example.com/x"), "TEMPO"); err == nil {
		t.Fatal("username without password accepted")
	}
}
