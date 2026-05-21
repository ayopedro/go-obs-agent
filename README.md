# go-obs-agent

A lightweight Go-based observability agent designed to analyze traces, metrics, and logs and surface actionable diagnostics.

## Objective

Build an intelligent agent that helps identify and explain system issues by correlating:

- distributed traces
- telemetry metrics
- application and infrastructure logs

The agent is intended to serve as a first-pass observability analyst for incident investigation and root cause diagnosis.

## What this repo contains

- `cmd/api/main.go` — entrypoint for an LLM-powered observability agent using Google ADK and Gemini
- `go.mod` — Go module configuration and dependencies

## Key capabilities

- Analyze trace data to isolate failing components and request paths
- Review metrics around incident timestamps to verify system health
- Inspect logs and correlate events with traces and metrics
- Produce concise, data-driven root cause summaries and remediation steps

## Prerequisites

- Go 1.26 or later
- `GEMINI_API_KEY` environment variable set for Gemini access

## Run locally

```bash
cd ./go-obs-agent/cmd/api
GEMINI_API_KEY="your_api_key_here" go run main.go
```
