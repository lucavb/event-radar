# Munich Event Radar

Personal, local-first event radar for relevant Munich AI, agent and developer
meetups. It collects public calendars, stores normalized events in SQLite and
exposes a single iCalendar feed. No deployment configuration is included.

## Sources

| Source | Ingestion | State |
| --- | --- | --- |
| OpenClaw Munich | Official Luma ICS | Active |
| Claude Code Munich | Claude Community Luma ICS, filtered to Munich, plus a tentative known-event seed | Active |
| AI Agents Munich | Public Meetup ICS | Active |
| Munich AI Developers Group | Public Meetup ICS | Active |
| AI Tinkerers Munich | Official Agents API | Disabled until attendance unlocks API access |
| New series | Optional SearXNG candidate discovery | Optional |

The application does not bypass bot protection and does not automate RSVPs.
Search results are unverified until explicitly approved in the review page.

## Local setup

```sh
cp config.example.env .env
set -a && . ./.env && set +a
go run ./cmd/munich-events check-config
go run ./cmd/munich-events sync
go run ./cmd/munich-events digest --dry-run
go run ./cmd/munich-events run
```

The feed is then available at:

```text
http://127.0.0.1:8080/calendar/<RADAR_FEED_TOKEN>.ics
```

Useful endpoints:

- `/healthz` returns failure when an enabled source most recently failed.
- `/status` lists per-source health, including
  `disabled_pending_attendance` for AI Tinkerers.
- `/metrics` returns basic Prometheus metrics.
- `/admin?token=<RADAR_ADMIN_TOKEN>` shows discovered candidates and allows
  approval or rejection. Keep this token separate from the calendar feed token.

## Discovery and verification

SearXNG is a broad discovery source. It must have JSON output enabled:

```yaml
search:
  formats:
    - html
    - json
```

The optional Gemini source uses two requests: Google Search finds likely direct
event URLs, then URL Context verifies each page into structured fields and
evidence. Configure `RADAR_GEMINI_ENDPOINT` with the full `generateContent`
endpoint. A direct Google endpoint can use `RADAR_GEMINI_API_KEY`; a proxy such
as TAIA can use `RADAR_GEMINI_TOKEN`. The application never publishes these
machine-generated events automatically: review them at `/admin`.

## Delivery

`digest --dry-run` only writes the digest to stdout. Real delivery requires
SMTP configuration and is intentionally opt-in. The SMTP values are compatible
with iCloud Mail's `smtp.mail.me.com:587` and an app-specific password.

The database stores a digest content hash, so a configured delivery is skipped
when the digest has not changed.

## AI Tinkerers activation

The account screenshot confirms that the API is suspended until an AI
Tinkerers event has been attended in the previous 90 days. After attendance:

1. Create a static `sk_…` Agent API key on
   <https://aitinkerers.org/developers/api-keys>.
2. Put it in `RADAR_AI_TINKERERS_API_KEY`.
3. Enable the API adapter in a follow-up change; its credentials must be
   supplied by the later deployment's secret store, never committed.

## Container build

```sh
docker build -t munich-events:local .
```

The Dockerfile is a portable build artifact only. Kubernetes, DNS, backups,
Apple Calendar subscription and infrastructure automation are deliberately
outside this repository and this scope.
