// Package redact removes secrets and common personal identifiers from text
// before it is sent to a model provider.
package redact

import "regexp"

type rule struct {
	re   *regexp.Regexp
	repl string
}

var rules = []rule{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED_PRIVATE_KEY]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`), "$1 [REDACTED_TOKEN]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b`), "[REDACTED_GITHUB_TOKEN]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED_SLACK_TOKEN]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED_API_KEY]"},
	// key=value or "key":"value" pairs whose key names a credential.
	{regexp.MustCompile(`(?i)((?:pass(?:word|wd)?|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret|authorization|connection[_-]?string)["']?\s*[:=]\s*["']?)[^\s"',;&}]+`), "${1}[REDACTED]"},
	// credentials embedded in URLs: scheme://user:pass@host
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s/:@]+:)[^\s/@]+@`), "${1}[REDACTED]@"},
	{regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), "[REDACTED_EMAIL]"},
}

// Text returns s with sensitive values replaced, and how many rules fired.
func Text(s string) (string, int) {
	hits := 0
	for _, r := range rules {
		if r.re.MatchString(s) {
			hits++
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s, hits
}
