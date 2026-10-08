package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/ayopedro/go-obs-agent/cmd/api/tools"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	model, err := gemini.NewModel(ctx, "gemini-3.1-flash-lite", &genai.ClientConfig{
		APIKey: os.Getenv("GEMINI_API_KEY"),
	})

	if err != nil {
		return fmt.Errorf("failed to create model: %w", err)
	}

	analyzeTracesTool, err := tools.NewAnalyzeTracesTool(nil)
	if err != nil {
		return fmt.Errorf("failed to create analyze_traces tool: %w", err)
	}

	queryMetricsTool, err := tools.NewQueryMetricsTool(nil)
	if err != nil {
		return fmt.Errorf("failed to create query_metrics tool: %w", err)
	}

	inspectLogsTool, err := tools.NewInspectLogsTool(nil)
	if err != nil {
		return fmt.Errorf("failed to create inspect_logs tool: %w", err)
	}

	cfg := llmagent.Config{
		Name:        "observability_agent",
		Model:       model,
		Description: "Agent to analyze and provide insights on observability data.",
		Instruction: `
		You are Shawn, a Senior Site Reliability Orchestrator Agent. Your role is to diagnose system issues by investigating application logs, metrics, and distributed traces using your provided tools.

		Follow this strict diagnostic protocol for every investigation:
		1. TRACE PATH: Start by inspecting traces or unique IDs provided by the user to isolate the failing component, database query, or downstream RPC call.
		2. CONTEXT CHECK: Once a failure point is isolated, verify the broader system health (e.g., CPU/Memory metrics or system logs) around that exact timestamp to rule out resource exhaustion.
		3. ROOT CAUSE SUMMARY: Synthesize your findings. State exactly what broke, why it broke, and provide a concrete remediation action.

		Rules of Engagement:
		- Data-Driven Only: Base your diagnoses strictly on the data returned by your tools. If a tool returns an empty dataset or error, explicitly state it. Never assume infrastructure states.
		- Token Efficiency: Keep your tool queries highly targeted. Restrict log/trace queries to the precise timeframe of the incident.
		- Tone: Professional, direct, and deeply technical. Skip conversational filler.
		`,
		Tools: []tool.Tool{
			analyzeTracesTool,
			queryMetricsTool,
			inspectLogsTool,
		},
	}

	a, err := llmagent.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}

	config := &launcher.Config{
		AgentLoader: agent.NewSingleLoader(a),
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		return fmt.Errorf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
	return nil
}
