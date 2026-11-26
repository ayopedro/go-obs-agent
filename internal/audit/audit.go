// Package audit writes structured audit records and carries the requesting
// identity through a context so every layer logs who asked for what.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
)

// Actor identifies the requester. Fields are platform IDs, not names.
type Actor struct {
	Platform string // slack, teams, cli
	Tenant   string
	Channel  string
	User     string
	EventID  string
}

type ctxKey struct{}

func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

func ActorFrom(ctx context.Context) Actor {
	a, _ := ctx.Value(ctxKey{}).(Actor)
	return a
}

// LogPrompts includes raw prompt text in records when true. Off by default
// because prompts may contain sensitive incident details.
var LogPrompts bool

// NewLogger returns a JSON logger for audit output.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// Log writes one audit record at info level, tagged with the actor from ctx.
func Log(ctx context.Context, event string, attrs ...any) {
	write(ctx, slog.LevelInfo, event, attrs)
}

// Warn writes a record for denied or failed actions.
func Warn(ctx context.Context, event string, attrs ...any) {
	write(ctx, slog.LevelWarn, event, attrs)
}

func write(ctx context.Context, level slog.Level, event string, attrs []any) {
	a := ActorFrom(ctx)
	base := []any{"audit", true, "event", event}
	if a != (Actor{}) {
		base = append(base, slog.Group("actor", "platform", a.Platform, "tenant", a.Tenant, "channel", a.Channel, "user", a.User, "event_id", a.EventID))
	}
	slog.Log(ctx, level, "audit", append(base, attrs...)...)
}

// Prompt describes a prompt without storing it unless LogPrompts is set.
func Prompt(text string) []any {
	sum := sha256.Sum256([]byte(text))
	attrs := []any{"prompt_sha256", hex.EncodeToString(sum[:8]), "prompt_chars", len([]rune(text))}
	if LogPrompts {
		attrs = append(attrs, "prompt", text)
	}
	return attrs
}

// Truncate shortens s for logging.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
