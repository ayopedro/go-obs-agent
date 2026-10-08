// Package chat adapts collaboration platforms to a shared investigation service.
package chat

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

type Investigator interface {
	Investigate(context.Context, string) (string, error)
}
type Reply func(context.Context, string) error
type job struct {
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

func (s *Service) Submit(id, prompt string, reply Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, at := range s.seen {
		if now.Sub(at) > 24*time.Hour {
			delete(s.seen, key)
		}
	}
	if _, ok := s.seen[id]; ok {
		return nil
	}
	if len(s.seen) >= 10000 {
		return ErrBusy
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}
	select {
	case s.jobs <- job{prompt, reply}:
		s.seen[id] = now
		return nil
	default:
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
			ctx, cancel := context.WithTimeout(s.ctx, 6*time.Minute)
			answer, err := s.engine.Investigate(ctx, j.prompt)
			if err != nil {
				log.Printf("investigation failed: %T", err)
				answer = "Investigation failed. Ask the service operator to check the server logs and configuration."
			}
			runes := []rune(answer)
			if len(runes) > 12000 {
				answer = string(runes[:12000]) + "\n[Response shortened; narrow the investigation.]"
			}
			if err := j.reply(ctx, answer); err != nil {
				log.Printf("chat delivery failed: %T", err)
			}
			cancel()
		}
	}
}
func (s *Service) Wait() { s.wg.Wait() }
