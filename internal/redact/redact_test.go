package redact

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	for _, c := range []struct{ in, gone string }{
		{"Authorization: Bearer abcdefghijklmnop1234", "abcdefghijklmnop1234"},
		{"password=hunter2 next", "hunter2"},
		{`{"api_key":"s3cr3tvalue"}`, "s3cr3tvalue"},
		{"aws AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGHIJKLMNOP"},
		{"gh ghp_abcdefghijklmnopqrstuvwx", "ghp_abcdefghijklmnopqrstuvwx"},
		{"jwt eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.sig-part", "eyJzdWIiOiIxMjM0"},
		{"postgres://app:dbpass@db:5432/x", "dbpass"},
		{"user jane.doe@example.com failed", "jane.doe@example.com"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", "MIIE"},
	} {
		got, n := Text(c.in)
		if strings.Contains(got, c.gone) || n == 0 {
			t.Errorf("Text(%q) = %q, %d", c.in, got, n)
		}
	}
}

func TestTextKeepsOperationalData(t *testing.T) {
	in := `trace_id=4bf92f3577b34da6a3ce929d0e0e4736 service=checkout error="pool exhausted" duration=1.2s token_count=5`
	if got, n := Text(in); got != in || n != 0 {
		t.Errorf("over-redacted: %q (%d)", got, n)
	}
}
