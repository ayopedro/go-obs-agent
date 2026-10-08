package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ayopedro/go-obs-agent/internal/agent"
)

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
	runtime, err := agent.Start(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	investigate := func(prompt string) error {
		answer, err := runtime.Investigate(ctx, prompt)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("investigation failed: %w", err)
		}
		fmt.Println(answer)
		return nil
	}
	if len(os.Args) > 1 {
		return investigate(strings.Join(os.Args[1:], " "))
	}
	fmt.Println("Observability agent. Enter an incident description; use /quit to exit.")
	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr <- scanner.Err()
		close(lines)
	}()
	for {
		fmt.Print("> ")
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return <-scanErr
			}
			line = strings.TrimSpace(line)
			if line == "/quit" {
				return nil
			}
			if line == "" {
				continue
			}
			if err := investigate(line); err != nil {
				return err
			}
		}
	}
}
