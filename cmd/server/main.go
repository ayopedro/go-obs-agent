package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ayopedro/go-obs-agent/internal/agent"
	"github.com/ayopedro/go-obs-agent/internal/chat"
)

type investigator struct{ cfg agent.Config }

func (i investigator) Investigate(ctx context.Context, prompt string) (string, error) {
	runtime, err := agent.Start(ctx, i.cfg)
	if err != nil {
		return "", err
	}
	defer runtime.Close()
	return runtime.Investigate(ctx, prompt)
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := agent.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	service := chat.NewService(ctx, investigator{cfg})
	defer func() { stop(); service.Wait() }()
	mux := http.NewServeMux()
	client := chat.HTTPClient()
	enabled := 0
	secret, token, workspace := os.Getenv("SLACK_SIGNING_SECRET"), os.Getenv("SLACK_BOT_TOKEN"), os.Getenv("SLACK_WORKSPACE_ID")
	if secret != "" || token != "" || workspace != "" {
		if secret == "" || token == "" || workspace == "" {
			return fmt.Errorf("Slack requires SLACK_SIGNING_SECRET, SLACK_BOT_TOKEN and SLACK_WORKSPACE_ID")
		}
		mux.Handle("/slack/events", &chat.Slack{SigningSecret: secret, BotToken: token, WorkspaceID: workspace, Service: service, Client: client})
		enabled++
	}
	appID, appSecret, tenant := os.Getenv("TEAMS_APP_ID"), os.Getenv("TEAMS_APP_SECRET"), os.Getenv("TEAMS_TENANT_ID")
	if appID != "" || appSecret != "" || tenant != "" {
		if appID == "" || appSecret == "" || tenant == "" {
			return fmt.Errorf("Teams requires TEAMS_APP_ID, TEAMS_APP_SECRET and TEAMS_TENANT_ID")
		}
		mux.Handle("/teams/messages", &chat.Teams{AppID: appID, AppSecret: appSecret, TenantID: tenant, Service: service, Client: client, Auth: &chat.TeamsAuth{AppID: appID, Client: client}})
		enabled++
	}
	if enabled == 0 {
		return fmt.Errorf("configure at least one chat interface; see .env.example")
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok\n")) })
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("chat server listening on %s", addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
