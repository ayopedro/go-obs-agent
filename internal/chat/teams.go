package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Teams struct {
	AppID, AppSecret, TenantID string
	Service                    *Service
	Auth                       *TeamsAuth
	Client                     *http.Client
	mu                         sync.Mutex
	token                      string
	expires                    time.Time
	tokenURL                   string
}
type activity struct {
	Type, ID, Text string
	ChannelID      string `json:"channelId"`
	ServiceURL     string `json:"serviceUrl"`
	Conversation   struct {
		ID string `json:"id"`
	} `json:"conversation"`
	From struct {
		ID string `json:"id"`
	} `json:"from"`
	Recipient struct {
		ID string `json:"id"`
	} `json:"recipient"`
	ChannelData struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	} `json:"channelData"`
}

var teamsMention = regexp.MustCompile(`(?s)<at>.*?</at>`)

func (t *Teams) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	var a activity
	if json.Unmarshal(body, &a) != nil {
		http.Error(w, "invalid activity", 400)
		return
	}
	if err := t.Auth.Verify(r.Context(), r.Header.Get("Authorization"), a.ServiceURL); err != nil {
		http.Error(w, "invalid connector authentication", 401)
		return
	}
	if a.ChannelID != "msteams" || a.ChannelData.Tenant.ID != t.TenantID {
		http.Error(w, "tenant or channel not allowed", 403)
		return
	}
	u, err := url.Parse(a.ServiceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() != "smba.trafficmanager.net" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		http.Error(w, "unsupported connector service URL", 403)
		return
	}
	if a.Type != "message" {
		w.WriteHeader(200)
		return
	}
	prompt := strings.TrimSpace(teamsMention.ReplaceAllString(a.Text, ""))
	if a.ID == "" || a.Conversation.ID == "" || a.From.ID == "" || a.Recipient.ID == "" || prompt == "" || len(prompt) > 16000 {
		http.Error(w, "invalid message", 400)
		return
	}
	err = t.Service.Submit("teams:"+t.TenantID+":"+a.Conversation.ID+":"+a.ID, prompt, func(ctx context.Context, text string) error {
		token, err := t.accessToken(ctx)
		if err != nil {
			return err
		}
		endpoint := strings.TrimRight(a.ServiceURL, "/") + "/v3/conversations/" + url.PathEscape(a.Conversation.ID) + "/activities/" + url.PathEscape(a.ID)
		return postJSON(ctx, t.Client, endpoint, token, map[string]any{"type": "message", "text": text, "textFormat": "plain", "replyToId": a.ID, "from": a.Recipient, "recipient": a.From, "conversation": a.Conversation}, nil)
	})
	if err != nil {
		http.Error(w, "service busy; retry later", 503)
		return
	}
	w.WriteHeader(200)
}
func (t *Teams) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expires) {
		return t.token, nil
	}
	endpoint := t.tokenURL
	if endpoint == "" {
		endpoint = "https://login.microsoftonline.com/" + url.PathEscape(t.TenantID) + "/oauth2/v2.0/token"
	}
	data := url.Values{"grant_type": {"client_credentials"}, "client_id": {t.AppID}, "client_secret": {t.AppSecret}, "scope": {"https://api.botframework.com/.default"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := t.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("Teams token request failed: HTTP %d", response.StatusCode)
	}
	var payload struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" || payload.Expires <= 60 {
		return "", fmt.Errorf("invalid Teams token response")
	}
	t.token = payload.Token
	t.expires = time.Now().Add(time.Duration(payload.Expires-60) * time.Second)
	return t.token, nil
}
