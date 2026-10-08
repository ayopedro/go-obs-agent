# go-obs-agent

A Go observability agent that investigates incidents using Tempo traces,
Prometheus metrics, and Loki logs. Telemetry functions are independent of the
LLM runtime; GitHub Copilot SDK provides orchestration and configurable inference.

## Project structure

- `internal/telemetry` — plain Go functions: `AnalyzeTraces`, `QueryMetrics`, and
  `InspectLogs`, accepting `context.Context`, an injectable HTTP client, and typed
  inputs. A nil client uses a 15-second timeout. No agent SDK dependency.
- `internal/agent` — Copilot tool adapters, diagnostic instructions, configuration,
  and session lifecycle.
- `cmd/api` — interactive console or one-shot incident investigation.

## Prerequisites

- Go 1.26.2 or later.
- Copilot CLI installed (`copilot` on PATH or `COPILOT_CLI_PATH` set).
- For Copilot inference: authenticate using `copilot login` and have appropriate
  Copilot access. Company policies must allow CLI/SDK use with incident telemetry.
- Running Tempo, Prometheus, and Loki containing the telemetry to investigate.

The SDK is pinned in `go.mod`. See the [official Go SDK](https://github.com/github/copilot-sdk/tree/main/go)
for compatible CLI setup and supported custom provider capabilities.

## Run with Copilot

From the repository root:

```bash
export LLM_PROVIDER=copilot
# Optional: choose a model available to your Copilot account.
# export LLM_MODEL=your-approved-model
export TEMPO_BASE_URL=http://localhost:3200
export PROMETHEUS_BASE_URL=http://localhost:9090
export LOKI_BASE_URL=http://localhost:3100

go run ./cmd/api
```

Enter an incident description, trace ID, relevant service labels, and incident
window where known. Use `/quit`, Ctrl+C, or EOF to exit. For one-shot use:

```bash
go run ./cmd/api 'Investigate trace TRACE_ID for checkout; correlate errors with metrics and logs.'
```

The app handles SIGTERM and cancels requests during shutdown. Each investigation
has a five-minute deadline. The agent can use only the three telemetry tools;
built-in shell, file-editing, and unrelated tools are excluded. Runtime config
discovery is disabled. Requests requiring managed approval are denied rather
than automatically approved.

## Custom model providers

Keep the same telemetry functions and Copilot orchestration while choosing a
custom inference endpoint. Supported provider types are `openai` (including
compatible gateways/local servers), `azure`, and `anthropic`.

```bash
export LLM_PROVIDER=openai
export LLM_MODEL=your-approved-model
export LLM_BASE_URL=https://your-approved-gateway.example/v1
export LLM_API_KEY=your-key
# Optional for openai/azure: completions (SDK default) or responses.
export LLM_WIRE_API=completions

go run ./cmd/api
```

Custom providers require a model and base URL. The API key is optional for
endpoints that do not require it, such as local servers. For Azure, optionally
set `LLM_AZURE_API_VERSION`; leave it empty for the SDK's default route. Use a
model supporting tool calling. Custom inference uses the provider's credentials
and billing, rather than a Copilot subscription; the Copilot CLI is still required.
Actual model availability and endpoint compatibility depend on your provider.
There is no automatic fallback to another provider.

`.env.example` documents all settings. `.env` files are not loaded automatically;
export variables in your shell. Gemini API keys and Google ADK are no longer used.

## Telemetry behavior and limits

The app queries existing data; it does not collect telemetry or start backends.
Backend clients currently do not configure authentication or tenant headers; use
local backends or a gateway providing these for the configured endpoints.

Trace results retain span parents and timestamps so the agent can correlate
metrics and logs around the incident. Numeric and named span status codes are
supported. Conclusions must cite evidence, distinguish hypotheses from confirmed
findings, and acknowledge missing or partial data.

Each backend response is limited to 4 MiB and each tool result to 64 KiB of JSON.
Logs are capped at 100 lines. Outputs exceeding the budget retain a subset and
set `truncated: true`; reaching the log query limit also marks results as
potentially incomplete. Trace `span_count` describes the full decoded trace;
log `total` describes returned lines. Narrow queries when results are partial.

## Checks

```bash
go test -race -cover ./...
go vet ./...
```

Tests exercise telemetry through local HTTP fixtures and test provider
configuration, tool restrictions, and cancellation without live LLM credentials.

## Local demo without telemetry backends

The fixture server serves a synthetic checkout incident with an exhausted database
connection pool. It is an API stub, not a real observability stack: it does not
execute PromQL/LogQL or enforce query time windows. It is intended to smoke-test
SDK integration and tool calls. Prometheus fixtures support only the three exact
metric names below; Loki always returns the incident log.

Terminal 1:

```bash
go run ./cmd/demo
```

Terminal 2 (authenticate with `copilot login` first if needed):

```bash
export LLM_PROVIDER=copilot
export TEMPO_BASE_URL=http://127.0.0.1:4319
export PROMETHEUS_BASE_URL=http://127.0.0.1:4319
export LOKI_BASE_URL=http://127.0.0.1:4319

go run ./cmd/api 'Investigate trace 0123456789abcdef0123456789abcdef for checkout. Compare db_pool_active_connections, db_pool_max_connections, and process_cpu_usage around the trace timestamps; inspect logs with {service="checkout"}. Cite the evidence and distinguish findings from hypotheses.'
```

Expected evidence: a failed `db.acquire_connection` span, active/max pool counts
both 100, CPU usage 0.12, and a connection-pool timeout log. The agent should identify
pool exhaustion, without asserting why the pool became exhausted. This run uses
live model inference and your configured account's allowance or provider billing.
Stop the demo with Ctrl+C. Switch the three backend URLs back when testing real data.
