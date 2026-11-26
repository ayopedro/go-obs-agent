// Package chat adapts collaboration platforms to a shared investigation service.
package chat

import (
	"context"
	"errors"
	"fmt"
	"github.com/ayopedro/go-obs-agent/internal/audit"
	"sync"
	"time"
)

type Investigator interface {
	Investigate(context.Context, string) (string, error)
}
type Reply func(context.Context, string) error
type job struct {
	actor  audit.Actor
	prompt string
	reply  Reply
}

// Service uses a bounded, in-memory queue. Each job is independent.
type Service struct {
	ctx    context.Context
	engine Investigator
	jobs   chan job
	mu     sync.Mutex
	seen   map[string]time.Time
	wg     sync.WaitGroup
}

func NewService(ctx context.Context, engine Investigator) *Service {
	s := &Service{ctx: ctx, engine: engine, jobs: make(chan job, 32), seen: map[string]time.Time{}}
	for range 2 {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

var ErrBusy = errors.New("investigation queue is full")

func (s *Service) Submit(id string, actor audit.Actor, prompt string, reply Reply) error {
	actor.EventID = id
	ctx := audit.WithActor(context.Background(), actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, at := range s.seen {
		if now.Sub(at) > 24*time.Hour {
			delete(s.seen, key)
		}
	}
	if _, ok := s.seen[id]; ok {
		audit.Log(ctx, "request.duplicate")
		return nil
	}
	if len(s.seen) >= 10000 {
		audit.Warn(ctx, "request.rejected", "reason", "dedup table full")
		return ErrBusy
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}
	select {
	case s.jobs <- job{actor, prompt, reply}:
		s.seen[id] = now
		audit.Log(ctx, "request.accepted", audit.Prompt(prompt)...)
		return nil
	default:
		audit.Warn(ctx, "request.rejected", "reason", "queue full")
		return ErrBusy
	}
}
func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			if s.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(audit.WithActor(s.ctx, j.actor), 6*time.Minute)
			started := time.Now()
			answer, err := s.engine.Investigate(ctx, j.prompt)
			if err != nil {
				audit.Warn(ctx, "investigation.failed", "error_type", fmt.Sprintf("%T", err), "duration_ms", time.Since(started).Milliseconds())
				answer = "Investigation failed. Ask the service operator to check the server logs and configuration."
			}
			if err == nil {
				audit.Log(ctx, "investigation.finished", "duration_ms", time.Since(started).Milliseconds(), "answer_chars", len([]rune(answer)))
			}
			runes := []rune(answer)
			if len(runes) > 12000 {
				answer = string(runes[:12000]) + "\n[Response shortened; narrow the investigation.]"
			}
			if err := j.reply(ctx, answer); err != nil {
				audit.Warn(ctx, "reply.failed", "error_type", fmt.Sprintf("%T", err))
			} else {
				audit.Log(ctx, "reply.delivered")
			}
			cancel()
		}
	}
}
func (s *Service) Wait() { s.wg.Wait() }
