# Observability stack

A minimal, tracing-only Grafana stack for Event Radar: Tempo stores traces,
Alloy receives OTLP spans from the app, and Grafana queries Tempo. There is no
Loki or Prometheus in this example.

## Quick start

```sh
docker compose up -d
```

Run the app with tracing enabled against the local Alloy:

```sh
RADAR_TRACING_ENABLED=true \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_SERVICE_NAME=event-radar \
go run ./cmd/event-radar run
```

Generate traffic (open the calendar or `/status`), then open Grafana at
http://localhost:3000, go to Explore → Tempo, and query
`service.name=event-radar`.

## Files

- `docker-compose.yaml`: tempo, alloy, and grafana on a single network.
- `tempo.config.yaml`: Tempo config with OTLP receivers on 4317/4318.
- `alloy.config.alloy`: River config receiving OTLP and exporting to Tempo.
- `grafana/provisioning/datasources/tempo.yaml`: provisioned Tempo datasource.
