# go-obs-agent

A Go observability agent that investigates incidents using Tempo traces,
Prometheus metrics, and Loki logs. It runs a small, bounded tool-use loop against
a model you choose (Azure OpenAI, GitHub Copilot, or Anthropic) and can call
additional tools from MCP servers.

## Project structure

- `internal/telemetry` — plain Go functions: `AnalyzeTraces`, `QueryMetrics`, and
  `InspectLogs`, accepting `context.Context`, an injectable HTTP client, and typed
  inputs. A nil client uses a 15-second timeout. No agent dependency.
- `internal/agent` — the tool-use loop, tool registry, MCP client, diagnostic
  instructions, and configuration.
- `internal/chat`, `cmd/server` — Slack and Teams adapters.
- `cmd/api` — interactive console or one-shot incident investigation.

## Prerequisites

- Go 1.26.2 or later.
- A model provider your organization approves (see [Model providers](#model-providers)).
- Running Tempo, Prometheus, and Loki containing the telemetry to investigate.

## Run

```bash
export LLM_PROVIDER=azure   # or copilot, anthropic; see Model providers
export LLM_BASE_URL=https://YOUR-RESOURCE.openai.azure.com
export LLM_MODEL=your-deployment-name
export LLM_API_KEY=your-azure-openai-key
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

Each investigation has a five-minute deadline and at most 15 model turns. Tool
errors are returned to the model rather than aborting the run, and each tool
result is capped at 64 KiB. The app handles SIGTERM and cancels requests during
shutdown. Only the telemetry tools and explicitly allowed MCP tools are available.

`.env.example` documents all settings; `.env` files are not loaded automatically,
so export variables in your shell.

## Model providers

Set `LLM_PROVIDER` (required) to one of:

| Provider | Settings | Notes |
| --- | --- | --- |
| `azure` | `LLM_BASE_URL` (resource endpoint, HTTPS), `LLM_MODEL` (**deployment name**), optional `LLM_API_KEY`, optional `LLM_AZURE_API_VERSION` | Azure OpenAI. Without `LLM_API_KEY` it signs in with Microsoft Entra ID (`DefaultAzureCredential`: managed identity, workload identity, or `az login`), so no secret is needed when hosted on Azure. The model must support function calling. |
| `copilot` | optional `LLM_MODEL`, `COPILOT_CLI_PATH`, `COPILOT_GITHUB_TOKEN` | GitHub Copilot SDK. Requires the Copilot CLI. Locally use `copilot login`; on a server set `COPILOT_GITHUB_TOKEN` for a GitHub account with Copilot access. Check your organization's Copilot policy before using a shared token. |
| `anthropic` | `LLM_MODEL`, `LLM_API_KEY`, optional `LLM_BASE_URL` | Anthropic API directly. Use only if approved. |

`azure` and `anthropic` run this repo's tool-use loop; `copilot` uses the Copilot
runtime's loop. All three use the same tools, allowlist, and audit records.

## MCP tools

Set `MCP_CONFIG` to a JSON file listing remote (streamable HTTP) MCP servers:

```json
{
  "servers": [
    {
      "name": "pagerduty",
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ${PAGERDUTY_TOKEN}" },
      "allowed_tools": ["list_incidents", "get_incident"]
    }
  ]
}
```

- Only tools listed in `allowed_tools` are exposed; startup fails if one is
  missing. There is no "allow all". Prefer read-only tools; a tool that changes
  state would run without human approval.
- Tools appear to the model as `mcp_<name>_<tool>`.
- `${VAR}` in header values is read from the environment, so secrets stay out of
  the file. Startup fails if a referenced variable is unset.
- Local (stdio) MCP servers are not supported; use a remote server or a gateway.
- Tool output is untrusted data to the model, like logs and span attributes.

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

Terminal 2:

```bash
# set LLM_PROVIDER and the settings for it (see Model providers)
export TEMPO_BASE_URL=http://127.0.0.1:4319
export PROMETHEUS_BASE_URL=http://127.0.0.1:4319
export LOKI_BASE_URL=http://127.0.0.1:4319

go run ./cmd/api 'Investigate trace 0123456789abcdef0123456789abcdef for checkout. Compare db_pool_active_connections, db_pool_max_connections, and process_cpu_usage around the trace timestamps; inspect logs with {service="checkout"}. Cite the evidence and distinguish findings from hypotheses.'
```

Expected evidence: a failed `db.acquire_connection` span, active/max pool counts
both 100, CPU usage 0.12, and a connection-pool timeout log. The agent should identify
pool exhaustion, without asserting why the pool became exhausted. This run uses
live model inference billed to your configured provider.
Stop the demo with Ctrl+C. Switch the three backend URLs back when testing real data.

Or with Docker Compose (copy `.env.example` to `.env` and set the `LLM_*` values first):

```bash
docker compose up -d demo
docker compose run --rm api 'Investigate trace 0123456789abcdef0123456789abcdef for checkout.'
docker compose --profile chat up server   # Slack/Teams server on 127.0.0.1:8080
docker compose down
```

## Teams and Slack

Run `go run ./cmd/server` to expose authenticated Teams and Slack message adapters.
Each accepted message runs a separate investigation and replies in the originating
conversation. Setup, app manifest templates, and current hosting limits are in
[Chat interfaces](docs/chat-interfaces.md). The local console remains available.

To deploy it (Docker, AWS ECS task definition, troubleshooting), see the
[Deployment guide](docs/deployment.md).
