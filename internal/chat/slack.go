package chat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Slack struct {
	SigningSecret, BotToken, WorkspaceID string
	Service                              *Service
	Client                               *http.Client
	apiURL                               string
}

var slackMention = regexp.MustCompile(`<@[A-Za-z0-9]+>`)

func (s *Slack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if !verifySlack(s.SigningSecret, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body, time.Now()) {
		http.Error(w, "invalid signature", 401)
		return
	}
	var event struct {
		Type, Challenge string
		TeamID          string `json:"team_id"`
		EventID         string `json:"event_id"`
		Event           struct {
			Type, Text, Channel, TS, Subtype string
			ThreadTS                         string `json:"thread_ts"`
			BotID                            string `json:"bot_id"`
		}
	}
	if json.Unmarshal(body, &event) != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if event.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": event.Challenge})
		return
	}
	if event.TeamID != s.WorkspaceID {
		http.Error(w, "workspace not allowed", 403)
		return
	}
	if event.Type != "event_callback" || event.Event.Type != "app_mention" || event.Event.BotID != "" || event.Event.Subtype != "" {
		w.WriteHeader(200)
		return
	}
	prompt := strings.TrimSpace(slackMention.ReplaceAllString(event.Event.Text, ""))
	if event.EventID == "" || event.Event.Channel == "" || event.Event.TS == "" || prompt == "" || len(prompt) > 16000 {
		http.Error(w, "invalid mention", 400)
		return
	}
	thread := event.Event.ThreadTS
	if thread == "" {
		thread = event.Event.TS
	}
	err = s.Service.Submit("slack:"+event.TeamID+":"+event.EventID, prompt, func(ctx context.Context, text string) error {
		endpoint := s.apiURL
		if endpoint == "" {
			endpoint = "https://slack.com/api/chat.postMessage"
		}
		var response struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		err := postJSON(ctx, s.Client, endpoint, s.BotToken, map[string]any{"channel": event.Event.Channel, "thread_ts": thread, "text": text, "unfurl_links": false, "unfurl_media": false}, &response)
		if err != nil {
			return err
		}
		if !response.OK {
			return fmt.Errorf("Slack rejected reply: %s", response.Error)
		}
		return nil
	})
	if err != nil {
		http.Error(w, "service busy; retry later", 503)
		return
	}
	w.WriteHeader(200)
}
func verifySlack(secret, timestamp, signature string, body []byte, now time.Time) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || secret == "" {
		return false
	}
	when := time.Unix(ts, 0)
	if when.Before(now.Add(-5*time.Minute)) || when.After(now.Add(5*time.Minute)) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}
