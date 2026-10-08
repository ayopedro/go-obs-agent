# Teams and Slack interfaces

The HTTP service exposes two adapters to the same `chat.Investigator` interface:

| Interface | Incoming route | Trigger | Response |
| --- | --- | --- | --- |
| Slack | `POST /slack/events` | Mention the installed bot in a channel | Reply in the originating thread |
| Teams | `POST /teams/messages` | Message the bot (mention it in shared scopes) | Reply to the originating activity |

Each request is authenticated, restricted to the configured workspace/tenant, and
acknowledged after admission to a bounded queue. Two workers each create a fresh
Copilot runtime/session per investigation, avoiding shared model history. Each
message is a standalone investigation; thread history is not loaded. Include the
trace ID, service labels, and time window in each request. The same LLM and
telemetry configuration is used by both interfaces.

## Run

Set the existing `LLM_*` and telemetry variables from `.env.example`, then configure
one or both platforms below. In a second terminal you can run `go run ./cmd/demo`
and point the backend URLs at `http://127.0.0.1:4319` for synthetic telemetry.

```bash
# Default is loopback; use this inside a container behind an HTTPS proxy.
export HTTP_ADDR=0.0.0.0:8080
go run ./cmd/server
```

Expose the service through HTTPS and route the two platform endpoints to it.
`GET /healthz` checks the HTTP process only, not credentials, LLM access, or backend
health. Copilot CLI must be installed on the server; provision its approved
credentials there or use the supported custom inference configuration.
The HTTP server never initiates an interactive login.

## Slack

1. Replace `YOUR_PUBLIC_HOST` in `integrations/slack/manifest.yaml` with your HTTPS
   host and create a Slack app from that manifest.
2. Install it in the intended workspace. Its bot scopes are `app_mentions:read`
   and `chat:write`; Events API subscribes to `app_mention`.
3. Set the signing secret from Basic Information, bot OAuth token from OAuth &
   Permissions, and allowed workspace ID:

```bash
export SLACK_SIGNING_SECRET=your-signing-secret
export SLACK_BOT_TOKEN=your-bot-token
export SLACK_WORKSPACE_ID=your-workspace-id
```

Start/restart the server before verifying the Events API request URL. Invite the
bot to a channel and mention it with an incident description. The adapter verifies
Slack's HMAC signature and rejects requests outside the five-minute timestamp
window. It ignores bot messages and handles signed URL verification challenges.

See [Slack request verification](https://docs.slack.dev/authentication/verifying-requests-from-slack/)
and [app mention setup](https://docs.slack.dev/app-management/quickstart-app-settings/).

## Teams

1. Register a single-tenant Entra application and Azure Bot with a client secret.
   Enable the Microsoft Teams channel on the bot resource. Set its messaging
   endpoint to `https://YOUR_PUBLIC_HOST/teams/messages`.
2. Set the application ID, secret, and allowed tenant:

```bash
export TEAMS_APP_ID=your-entra-application-id
export TEAMS_APP_SECRET=your-application-secret
export TEAMS_TENANT_ID=your-tenant-id
```

3. Replace placeholders in `integrations/teams/manifest.json`. The Teams app UUID
   and bot application ID are separate settings. Add a 32×32 transparent outline
   icon (`outline.png`) and 192×192 color icon (`color.png`). Provide your actual
   organization, website, privacy, and terms URLs. Zip the manifest and icons at
   the archive root and upload the custom app through Teams Developer Portal or
   your organization's approved app publishing process.
4. Install the app and send the bot an incident description. Mention it in a team
   or group chat. The service responds asynchronously to that activity.

Incoming Connector JWTs require RS256, the Bot Framework issuer, this app's
audience, expiration/not-before, a matching service URL claim, and an `msteams`
key endorsement. Signing keys are fetched from Microsoft's fixed HTTPS endpoint
and cached for an hour. Replies use a single-tenant client-credentials OAuth token.
Only the public-cloud `smba.trafficmanager.net` Connector service is supported;
sovereign clouds, managed identity, multi-tenant installations, and Bot Framework
Emulator authentication are not implemented. There is no authentication bypass.

See [Microsoft's Connector authentication requirements](https://learn.microsoft.com/en-us/azure/bot-service/rest-api/bot-framework-rest-connector-authentication).

## Operational scope

This initial service is for one configured organization and telemetry stack.
Workspace/tenant membership grants access to those telemetry queries: restrict app
installation and channel membership to the intended audience. It does not yet
provide per-user telemetry authorization or customer-specific credentials.
Grafana Cloud still needs the backend authentication support described in the
repository's telemetry limitations.

The queue holds 32 waiting jobs and has two workers. Duplicate event/activity IDs
are remembered for 24 hours, up to 10,000 entries; overloaded requests return 503.
The queue and deduplication are in memory: use one server instance, and expect
pending jobs to be lost on restart. Shutdown cancels running work. Delivery
failures are logged without automatic retry; there is no durable result history.
Results longer than 12,000 characters are explicitly shortened. Add durable jobs,
shared deduplication, delivery retries, and appropriate hosted Copilot credentials
before scaling this into a production service.

No cloud resources or platform apps are created by running the Go service.
Platform tokens belong in your secret manager/environment, not manifests or logs.

## Validation

```bash
go test -race ./...
go vet ./...
```

Tests cover signed Slack requests, threaded replies, deduplication, queue pressure,
Teams signature/claim/endorsement rejection, tenant restrictions, OAuth reply
routing, and the existing telemetry adapters. Platform responses and model
investigations are faked; live Slack/Teams registration remains an external check.
