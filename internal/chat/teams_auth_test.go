package chat

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTeamsAuthKeyFetch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	var hits atomic.Int32
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{"kid": "good", "kty": "RSA", "n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes()), "endorsements": []string{"msteams"}},
			{"kid": "ec", "kty": "EC"},
			{"kid": "small", "kty": "RSA", "n": enc([]byte{1, 2, 3}), "e": enc([]byte{1, 0, 1})},
			{"kid": "weakexp", "kty": "RSA", "n": enc(key.N.Bytes()), "e": enc([]byte{1})},
		}})
	}))
	defer server.Close()

	auth := &TeamsAuth{Client: server.Client(), keysURL: server.URL}
	got, err := auth.key(t.Context(), "good")
	if err != nil {
		t.Fatal(err)
	}
	if got.key.N.Cmp(key.N) != 0 || got.key.E != key.E || len(got.endorsements) != 1 {
		t.Fatal("parsed key mismatch")
	}
	if _, err := auth.key(t.Context(), "good"); err != nil || hits.Load() != 1 {
		t.Fatalf("expected cached key, err=%v hits=%d", err, hits.Load())
	}
	for _, kid := range []string{"ec", "small", "weakexp", "missing"} {
		if _, err := auth.key(t.Context(), kid); err == nil {
			t.Fatalf("key %q accepted", kid)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("unknown-key lookups must be throttled, hits=%d", hits.Load())
	}

	status = http.StatusInternalServerError
	failing := &TeamsAuth{Client: server.Client(), keysURL: server.URL}
	if _, err := failing.key(t.Context(), "good"); err == nil {
		t.Fatal("failed lookup accepted")
	}
}
