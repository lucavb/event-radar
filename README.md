# Event Radar

Event Radar is a local-first event aggregator. It imports configured
iCalendar feeds and optional API sources into SQLite, optionally discovers
candidate events through SearXNG or Gemini, and serves one calendar feed with
an optional email digest.

It is not tied to a city, language, or topic. Gemini is optional; the core
aggregator does not require an AI provider.

## Pipeline

Configured feeds are treated as anchored events. Discovery results are stored
as candidates. Gemini can verify a candidate's exact page and supply explicit
date, location, and evidence. Candidates are never published automatically:
use the authenticated review page to approve them.

Without Gemini, SearXNG candidates remain visible as `unverified` candidates,
but cannot be approved until they contain verification evidence.

## Quick start

```sh
cp config.example.env .env
set -a && . ./.env && set +a
go run ./cmd/event-radar check-config
go run ./cmd/event-radar sync
go run ./cmd/event-radar digest --dry-run
go run ./cmd/event-radar run
```

The calendar is available at:

```text
http://127.0.0.1:8080/calendar/<RADAR_FEED_TOKEN>.ics
```

Endpoints:

- `/healthz` reports source failures.
- `/status` reports source health and candidate counts.
- `/metrics` exposes Prometheus gauges.
- `/admin?token=<RADAR_ADMIN_TOKEN>` reviews candidates.

## Configuration

Structured values use JSON so URLs and search text do not depend on custom
delimiter escaping.

- `RADAR_ICS_FEEDS`: JSON objects with `name`, `url`, `anchor`,
  `filter_location`, and `force_confirmed`.
- `RADAR_LOCATION_ALIASES`: aliases used by filtered feeds.
- `RADAR_EVENT_CRITERIA`: scope passed to Gemini verification.
- `RADAR_SEARXNG_URL` and `RADAR_SEARXNG_QUERIES`: optional discovery.
- `RADAR_GEMINI_ENDPOINT`, one of `RADAR_GEMINI_API_KEY` or
  `RADAR_GEMINI_TOKEN`, and `RADAR_GEMINI_DISCOVERY_QUERIES`: optional
  discovery and verification.
- `RADAR_RELEVANCE_WEIGHTS`: JSON object of lowercase terms and positive
  integer weights.
- `RADAR_APP_NAME`, `RADAR_CALENDAR_PRODID`, and `RADAR_TIMEZONE`: output
  branding and formatting.
- `RADAR_ADMIN_TOKEN`: separate token for candidate moderation.
- `RADAR_SMTP_*` and `RADAR_DIGEST_RECIPIENT`: optional digest delivery.

See [`config.example.env`](config.example.env) for a generic configuration.
The original Munich AI use case is available at
[`examples/munich-ai`](examples/munich-ai).

## Storage and delivery

SQLite stores events, source health, candidates, and delivery hashes. The
digest is opt-in and `digest --dry-run` never sends email. Existing databases
retain their event UIDs; newly created events use the `event-radar-` UID
prefix.

## Container

```sh
docker build -t event-radar:local .
```

The repository contains no deployment, DNS, backup, or calendar-subscription
automation.

## License

MIT. See [`LICENSE`](LICENSE).
