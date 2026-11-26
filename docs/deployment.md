# Deployment guide

This guide explains how to run the chat server (`cmd/server`) so people can talk
to the agent from Slack and Microsoft Teams.

## How it works

```
Slack / Teams  --HTTPS webhook-->  chat server  --> queue (32 jobs, 2 workers)
                                                        |
                                              agent loop (internal/agent)
                                         |             |                |
                                  model provider  telemetry tools    MCP tools
                                  (Azure OpenAI,  (Tempo, Prometheus,  (remote servers,
                                   Copilot, ...)   Loki)                allowlisted)
                                                        |
Slack / Teams  <-------------- reply posted back -------+
```

- The server is an ordinary web service. Slack and Teams call it; it replies
  asynchronously after the investigation finishes.
- The agent loop sends the conversation to the model, runs the tools it asks for,
  returns the results, and repeats until it answers (max 15 turns, 5 minutes).
  With `LLM_PROVIDER=copilot` the Copilot runtime runs this loop instead.
- `azure` and `anthropic` need only the static Go binary. `copilot` also needs
  Node and the Copilot CLI (see Docker below).
- Incident data is sent to the model provider you choose. Use only a provider
  your organization has approved for it.

## Configuration

All settings are environment variables; see `.env.example`.

```bash
LLM_PROVIDER=azure
LLM_BASE_URL=https://YOUR-RESOURCE.openai.azure.com
LLM_MODEL=<Azure OpenAI deployment name; must support function calling>
LLM_API_KEY=<secret>   # omit on Azure hosting to use managed identity (Entra ID)
# For copilot or anthropic, see the Model providers table in the README.

TEMPO_BASE_URL=...
PROMETHEUS_BASE_URL=...
LOKI_BASE_URL=...

MCP_CONFIG=/path/to/mcp.json   # optional, see the README

HTTP_ADDR=0.0.0.0:8080   # default is 127.0.0.1:8080, unreachable from outside a container
```

Then add the variables for each platform you use (see
[Chat interfaces](chat-interfaces.md)). A platform needs **all** of its variables
or none; a partial set makes the server exit at startup. At least one platform is
required.

| Platform | Variables |
| --- | --- |
| Slack | `SLACK_SIGNING_SECRET`, `SLACK_BOT_TOKEN`, `SLACK_WORKSPACE_ID` |
| Teams | `TEAMS_APP_ID`, `TEAMS_APP_SECRET`, `TEAMS_TENANT_ID` |

Keep secrets (`LLM_API_KEY`, `COPILOT_GITHUB_TOKEN`, Slack token and signing secret, Teams secret, and any
MCP tokens referenced as `${VAR}`) in a secret manager, never in the image,
manifests, or git.

### MCP servers

MCP tools are loaded from a JSON file (`MCP_CONFIG`), described in the
[README](../README.md#mcp-tools). Each server needs an explicit `allowed_tools`
list. The server reads the file at startup of each investigation, so in a
container, mount it or bake a non-secret copy into the image and pass tokens as
environment variables. Prefer read-only tools: anything that changes state runs
without human approval.

## Run locally

Test the agent before adding chat platforms, using the synthetic telemetry demo:

```bash
go run ./cmd/demo                      # terminal 1
export TEMPO_BASE_URL=http://127.0.0.1:4319
export PROMETHEUS_BASE_URL=http://127.0.0.1:4319
export LOKI_BASE_URL=http://127.0.0.1:4319
# set LLM_PROVIDER and its settings, then:
go run ./cmd/api 'Investigate trace 0123456789abcdef0123456789abcdef for checkout.'
```

To try Slack from your machine, run `go run ./cmd/server` and expose port 8080
with a tunnel such as ngrok. Teams requires a real Azure Bot and a public HTTPS
endpoint.

## Docker

The `Dockerfile` builds `cmd/server` into a small distroless image that runs as a
non-root user.

```bash
docker build -t go-obs-agent .
docker run --env-file .env -p 8080:8080 go-obs-agent
```

The image has no shell or curl, so check `GET /healthz` from outside the
container. `.env` is for local runs only; it is excluded from the image by
`.dockerignore`.

## AWS ECS (Fargate)

`deploy/ecs-task-definition.json` is a template. Non-secret values are in
`environment`; secrets are read from AWS Secrets Manager.

1. Replace `ACCOUNT_ID`, `REGION`, and the `REPLACE_*` values (Azure OpenAI
   endpoint and deployment name). Change `LLM_PROVIDER` if you do not use `azure`.
   Outside Azure there is no managed identity, so keep `LLM_API_KEY` as a secret.
   Remove the Slack or Teams variables and secrets you do not use.
2. Create an ECR repository, then build, tag, and push the image.
3. Create the Secrets Manager secrets referenced in `secrets`, the
   `/ecs/go-obs-agent` log group, and two IAM roles. The execution role needs
   `AmazonECSTaskExecutionRolePolicy` and `secretsmanager:GetSecretValue` on those
   secrets.
4. Register it:
   `aws ecs register-task-definition --cli-input-json file://deploy/ecs-task-definition.json`
5. Create a service behind an HTTPS Application Load Balancer. Use a target-group
   health check on `/healthz`.
6. Make sure the tasks can reach Tempo, Prometheus, Loki, and any MCP servers
   (same VPC or routed).
7. Set the Slack Events URL to `https://YOUR_HOST/slack/events` and the Azure Bot
   messaging endpoint to `https://YOUR_HOST/teams/messages`.

**Run exactly one task.** The job queue and duplicate-event tracking are in
memory. Use `desiredCount: 1`, minimum healthy percent 0, and maximum percent 100
so deployments never run two copies. Pending jobs are lost on restart.

`/healthz` only confirms the process is up, not that credentials, the LLM, or
telemetry backends work.

## Access control

Slack and Teams only accept requests from the configured workspace or tenant.
Within it, restrict who can start investigations:

| Variable | Meaning |
|---|---|
| `ALLOWED_USERS` | Comma-separated user IDs (Slack `U...`, Teams `29:...`). |
| `ALLOWED_CHANNELS` | Comma-separated channel or conversation IDs. |
| `ACCESS_ALLOW_ALL` | `true` allows everyone in the workspace or tenant. |

When both lists are set, a request must match both. The server refuses to start
if neither list is set and `ACCESS_ALLOW_ALL` is not `true`. Denied requests are
dropped without a reply and logged as `request.denied` in the audit log.

## Data redaction

Prompts, tool results and tool errors are scrubbed before they reach the model
provider. It replaces:

- private keys, JWTs, `Bearer`/`Basic` tokens, AWS keys, GitHub, Slack and `sk-` API keys
- values of keys named like `password`, `secret`, `token`, `api_key`, `authorization`
- passwords inside URLs (`scheme://user:pass@host`)
- email addresses

Redaction runs on every provider and is always on. Each `tool.call` audit record
includes a `redactions` count. It is pattern-based, so it does not catch names,
IP addresses, or secrets with no recognizable shape. Combine it with
a tight `allowed_tools` list and with your provider's data-handling terms.

## Audit logging

The server writes one JSON record per line to stdout (CloudWatch on ECS). Audit
records have `"audit": true` and an `event` name; on chat requests they include an
`actor` with platform, tenant, channel, user, and event ID.

| Event | Meaning |
| --- | --- |
| `request.accepted` / `request.duplicate` / `request.rejected` | Message admitted to the queue, ignored as a repeat, or refused (queue full). |
| `request.denied` | Failed signature or authentication, or wrong workspace or tenant (warning). |
| `agent.started` / `agent.completed` | Model, prompt hash and length; then outcome, turns, tool calls, token counts, duration. |
| `tool.call` | Tool name, arguments (truncated to 1000 characters), duration, result size, success or error. |
| `investigation.finished` / `investigation.failed` | End-to-end result and duration. |
| `reply.delivered` / `reply.failed` | Whether the answer reached the chat. |

Prompts are recorded as a short hash and length only. Set `AUDIT_LOG_PROMPTS=true`
to include the text. Tool arguments are always logged, so avoid putting secrets in
MCP tool inputs. Tool results, API keys, and tokens are never logged. Set a
retention period on the CloudWatch log group; the service keeps no audit database.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `set LLM_PROVIDER to azure, copilot, or anthropic` | `LLM_PROVIDER` is required. |
| `LLM_PROVIDER=azure requires LLM_MODEL ... and LLM_BASE_URL` | Set the deployment name and the HTTPS resource endpoint. |
| Azure 401/403 | Wrong key, or Entra identity lacks the *Cognitive Services OpenAI User* role on the resource. |
| Azure 404 | `LLM_MODEL` must be the deployment name (not the model name), and the API version must be supported by the resource. |
| `Copilot CLI not found` | Use the `with-copilot` Docker target, or install the CLI and set `COPILOT_CLI_PATH`. |
| Copilot auth errors | Set `COPILOT_GITHUB_TOKEN` for an account with Copilot access. |
| `MCP server "x" must list allowed_tools` / `does not provide allowed tool` | Fix `MCP_CONFIG`; names must match the server's tool names. |
| `header ... references unset variable` | An MCP header uses `${VAR}` that is not set. |
| Server exits: `Slack requires ...` / `Teams requires ...` | Only some variables for that platform are set. |
| `configure at least one chat interface` | No Slack or Teams variables are set. |
| Slack URL verification fails | Server not running or not publicly reachable over HTTPS, or wrong signing secret. Start the server before verifying. |
| Slack/Teams get no reply | Check logs; replies are not retried. A 503 means the queue is full. |
| Teams 401 | Wrong app ID, wrong tenant, or an unsupported setup (see limits below). |
| Empty or partial answers | Telemetry backends unreachable or results truncated; narrow the query. |

## Limits and security

- One organization and one telemetry stack. Anyone who can message the bot in the
  allowed workspace or tenant can trigger telemetry and MCP tool calls, so limit
  who can install the app and use it.
- Backend clients send no authentication or tenant headers; use private backends
  or a gateway.
- Teams supports only single-tenant bots in the public Microsoft cloud.
- No durable queue, retries, or result history. See
  [Chat interfaces](chat-interfaces.md#operational-scope).
