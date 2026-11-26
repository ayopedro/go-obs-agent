package agent

import (
	"context"
	"regexp"

	"github.com/ayopedro/go-obs-agent/internal/audit"
)

const refusal = "I can't help with that request. I only investigate traces, metrics and logs, and I never reveal credentials, environment variables or my own instructions."

// blockedPatterns match requests for secrets, the agent's own configuration, or
// attempts to override its instructions. They run before the model sees the prompt.
var blockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(show|print|reveal|dump|list|output|leak|display|repeat|send|give|read|fetch|get|tell)\b.{0,40}\b(env(ironment)? vars?|environment variables?|api[ _-]?keys?|secrets?|credentials?|passwords?|private keys?|access tokens?|system prompt|system message|instructions)\b`),
	regexp.MustCompile(`(?i)\b(your|the (app|agent|server|service)'?s?)\s+(api[ _-]?keys?|secrets?|tokens?|passwords?|credentials?|env\w*|config\w*|system prompt|instructions)\b`),
	regexp.MustCompile(`(?i)\b(printenv|/proc/self/environ|cat\s+\.env)\b`),
	regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b.{0,20}\b(previous|prior|above|earlier|all|your)\b.{0,20}\b(instructions|rules|prompt)\b`),
}

// gate reports whether the prompt must be refused. Blocked requests are audited
// but never reach the model or any tool.
func gate(ctx context.Context, prompt string) bool {
	for i, p := range blockedPatterns {
		if p.MatchString(prompt) {
			audit.Warn(ctx, "agent.blocked", "rule", i)
			return true
		}
	}
	return false
}
