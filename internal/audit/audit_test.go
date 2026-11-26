package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestActorRoundTrip(t *testing.T) {
	want := Actor{Platform: "slack", User: "U1"}
	if got := ActorFrom(WithActor(context.Background(), want)); got != want {
		t.Fatalf("got %+v", got)
	}
	if got := ActorFrom(context.Background()); got != (Actor{}) {
		t.Fatalf("expected zero actor, got %+v", got)
	}
}

func TestLogIncludesActor(t *testing.T) {
	buf := capture(t)
	ctx := WithActor(context.Background(), Actor{Platform: "teams", User: "u42"})
	Log(ctx, "investigate", "k", "v")
	Warn(context.Background(), "denied")
	out := buf.String()
	for _, want := range []string{`"event":"investigate"`, `"user":"u42"`, `"k":"v"`, `"level":"WARN"`, `"event":"denied"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if strings.Count(out, `"actor"`) != 1 {
		t.Fatalf("actor group should only appear when set: %s", out)
	}
}

func TestPromptHidesTextByDefault(t *testing.T) {
	attrs := Prompt("secret incident")
	for _, a := range attrs {
		if a == "secret incident" {
			t.Fatal("prompt logged by default")
		}
	}
	LogPrompts = true
	t.Cleanup(func() { LogPrompts = false })
	if got := Prompt("secret incident"); len(got) != len(attrs)+2 {
		t.Fatalf("prompt not included when enabled: %v", got)
	}
}

func TestTruncate(t *testing.T) {
	if Truncate("héllo", 10) != "héllo" || Truncate("héllo", 2) != "hé…" {
		t.Fatal("unexpected truncation")
	}
	if NewLogger() == nil {
		t.Fatal("nil logger")
	}
}
