package chat

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fakeInvestigator struct {
	calls    atomic.Int32
	received chan string
	block    bool
}

func (f *fakeInvestigator) Investigate(ctx context.Context, prompt string) (string, error) {
	f.calls.Add(1)
	if f.received != nil {
		f.received <- prompt
	}
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "pool exhausted", nil
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testService(t *testing.T, engine Investigator) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	s := NewService(ctx, engine)
	t.Cleanup(func() { cancel(); s.Wait() })
	return s
}
func signedSlack(body string, when time.Time) *http.Request {
	r := httptest.NewRequest("POST", "/slack/events", strings.NewReader(body))
	ts := fmt.Sprint(when.Unix())
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte("v0:" + ts + ":" + body))
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return r
}
func TestSlackAuthenticationAndThreadReply(t *testing.T) {
	engine := &fakeInvestigator{received: make(chan string, 2)}
	sent := make(chan map[string]any, 2)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://slack.com/api/chat.postMessage" || r.Header.Get("Authorization") != "Bearer bot-token" {
			t.Error("incorrect Slack destination or auth")
		}
		var message map[string]any
		_ = json.NewDecoder(r.Body).Decode(&message)
		sent <- message
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	slack := &Slack{SigningSecret: "secret", BotToken: "bot-token", WorkspaceID: "T1", Service: testService(t, engine), Client: client}
	body := `{"type":"event_callback","team_id":"T1","event_id":"Ev1","event":{"type":"app_mention","text":"<@U1> Investigate trace abc","channel":"C1","ts":"123","thread_ts":"100"}}`
	for range 2 {
		w := httptest.NewRecorder()
		slack.ServeHTTP(w, signedSlack(body, time.Now()))
		if w.Code != 200 {
			t.Fatalf("event rejected: %d", w.Code)
		}
	}
	select {
	case prompt := <-engine.received:
		if prompt != "Investigate trace abc" {
			t.Fatal(prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("job missing")
	}
	select {
	case message := <-sent:
		if message["thread_ts"] != "100" || message["channel"] != "C1" || message["text"] != "pool exhausted" {
			t.Fatalf("wrong reply: %v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("reply missing")
	}
	if engine.calls.Load() != 1 {
		t.Fatal("duplicate investigation")
	}
	for _, req := range []*http.Request{signedSlack(body, time.Now().Add(-10*time.Minute)), httptest.NewRequest("POST", "/slack/events", strings.NewReader(body))} {
		w := httptest.NewRecorder()
		slack.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal("unauthenticated event accepted")
		}
	}
	w := httptest.NewRecorder()
	slack.ServeHTTP(w, signedSlack(strings.Replace(body, `"T1"`, `"T2"`, 1), time.Now()))
	if w.Code != 403 {
		t.Fatal("foreign workspace accepted")
	}
	w = httptest.NewRecorder()
	slack.ServeHTTP(w, signedSlack(`{"type":"url_verification","challenge":"challenge"}`, time.Now()))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "challenge") {
		t.Fatal("verification failed")
	}
}

func TestQueueBackpressure(t *testing.T) {
	engine := &fakeInvestigator{block: true, received: make(chan string, 2)}
	s := testService(t, engine)
	reply := func(context.Context, string) error { return nil }
	for i := range 2 {
		if err := s.Submit(fmt.Sprint(i), "incident", reply); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		<-engine.received
	}
	for i := range 32 {
		if err := s.Submit(fmt.Sprint(i+2), "incident", reply); err != nil {
			t.Fatal(err)
		}
	}
	if s.Submit("overflow", "incident", reply) != ErrBusy {
		t.Fatal("queue not bounded")
	}
	if s.Submit("0", "incident", reply) != nil {
		t.Fatal("duplicate should be acknowledged")
	}
}

func TestTeamsConnectorAuthentication(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	auth := &TeamsAuth{AppID: "app", keys: map[string]signingKey{"key": {&key.PublicKey, []string{"msteams"}}}, refreshed: time.Now()}
	serviceURL := "https://smba.trafficmanager.net/teams/"
	makeToken := func(audience, url string, expiry time.Time, method jwt.SigningMethod) string {
		claims := connectorClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://api.botframework.com", Audience: jwt.ClaimStrings{audience}, ExpiresAt: jwt.NewNumericDate(expiry), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute))}, ServiceURL: url}
		token := jwt.NewWithClaims(method, claims)
		token.Header["kid"] = "key"
		var signingKey any = key
		if method == jwt.SigningMethodHS256 {
			signingKey = []byte("bad")
		}
		signed, err := token.SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + signed
	}
	valid := makeToken("app", serviceURL, time.Now().Add(time.Hour), jwt.SigningMethodRS256)
	if err := auth.Verify(t.Context(), valid, serviceURL); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{makeToken("other", serviceURL, time.Now().Add(time.Hour), jwt.SigningMethodRS256), makeToken("app", "https://attacker.example/", time.Now().Add(time.Hour), jwt.SigningMethodRS256), makeToken("app", serviceURL, time.Now().Add(-time.Hour), jwt.SigningMethodRS256), makeToken("app", serviceURL, time.Now().Add(time.Hour), jwt.SigningMethodHS256)} {
		if auth.Verify(t.Context(), token, serviceURL) == nil {
			t.Fatal("invalid connector token accepted")
		}
	}
	auth.keys["key"] = signingKey{&key.PublicKey, nil}
	if auth.Verify(t.Context(), valid, serviceURL) == nil {
		t.Fatal("missing endorsement accepted")
	}
}

func TestTeamsActivityQueuesReply(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	auth := &TeamsAuth{AppID: "app", keys: map[string]signingKey{"key": {&key.PublicKey, []string{"msteams"}}}, refreshed: time.Now()}
	serviceURL := "https://smba.trafficmanager.net/teams/"
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, connectorClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://api.botframework.com", Audience: jwt.ClaimStrings{"app"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute))}, ServiceURL: serviceURL})
	token.Header["kid"] = "key"
	signed, _ := token.SignedString(key)
	sent := make(chan map[string]any, 1)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "login.microsoftonline.com") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"connector-token","expires_in":3600}`))}, nil
		}
		if r.URL.String() != serviceURL+"v3/conversations/conversation/activities/message" || r.Header.Get("Authorization") != "Bearer connector-token" {
			t.Error("incorrect Teams reply endpoint")
		}
		var message map[string]any
		_ = json.NewDecoder(r.Body).Decode(&message)
		sent <- message
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	engine := &fakeInvestigator{}
	teams := &Teams{AppID: "app", AppSecret: "secret", TenantID: "tenant", Auth: auth, Client: client, Service: testService(t, engine)}
	body := `{"type":"message","id":"message","text":"<at>Bot</at> Investigate trace abc","channelId":"msteams","serviceUrl":"` + serviceURL + `","conversation":{"id":"conversation"},"from":{"id":"user"},"recipient":{"id":"bot"},"channelData":{"tenant":{"id":"tenant"}}}`
	request := func(body string) *http.Request {
		r := httptest.NewRequest("POST", "/teams/messages", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+signed)
		return r
	}
	for range 2 {
		w := httptest.NewRecorder()
		teams.ServeHTTP(w, request(body))
		if w.Code != 200 {
			t.Fatalf("Teams rejected: %d %s", w.Code, w.Body.String())
		}
	}
	select {
	case message := <-sent:
		if message["text"] != "pool exhausted" || message["from"].(map[string]any)["id"] != "bot" {
			t.Fatalf("wrong Teams reply: %v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("reply missing")
	}
	if engine.calls.Load() != 1 {
		t.Fatal("duplicate Teams job")
	}

	foreign := strings.Replace(body, `"id":"tenant"`, `"id":"other"`, 1)
	w := httptest.NewRecorder()
	teams.ServeHTTP(w, request(foreign))
	if w.Code != 403 {
		t.Fatal("foreign tenant accepted")
	}
}
