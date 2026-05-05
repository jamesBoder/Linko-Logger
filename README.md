# Linko — URL Shortener Service

A production-grade URL shortener built in Go, demonstrating modern DevOps practices: structured logging, distributed tracing, Prometheus metrics, and a full observability stack.

> Built as a portfolio project showcasing Go backend engineering and observability-first development.

---

## Features

- **URL shortening** — submit a long URL, receive a short 6-character code
- **Authenticated API** — bcrypt password hashing, context-scoped user extraction
- **Redirect handler** — validates destination reachability before redirecting
- **Request tracing** — OpenTelemetry spans exported to Jaeger
- **Metrics** — Prometheus counters by method/path/status
- **Structured logging** — JSON to file (with log rotation), colored output to stderr
- **Security-aware logging** — auto-redacts passwords, API keys, embedded URL credentials, client IPs
- **pprof endpoints** — CPU and memory profiling under auth
- **Graceful shutdown** — SIGINT/SIGTERM handled with 5-second drain

---

## Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.26 |
| Logging | `log/slog` + `tint` (colored stderr) + `lumberjack` (rotation) |
| Tracing | OpenTelemetry → Jaeger (OTLP gRPC) |
| Metrics | Prometheus `client_golang` |
| Auth | `golang.org/x/crypto/bcrypt` |
| Storage | File-based key-value store (`./data/`) |
| Observability stack | Docker Compose: Prometheus · Grafana · Jaeger · Node Exporter |

---

## Quick Start

```bash
# 1. Start the observability stack
docker compose up -d

# 2. Build and run the server
go build -ldflags "-X boot.dev/linko/internal/build.GitSHA=$(git rev-parse HEAD) \
  -X boot.dev/linko/internal/build.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o linko .

LINKO_LOG_FILE=linko.out.log ENV=development ./linko --port 8899
```

**Observability endpoints:**

| Service | URL |
|---|---|
| Linko app | http://localhost:8899 |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 (admin / admin) |
| Jaeger UI | http://localhost:16686 |

---

## API Reference

All `/api/*` routes require HTTP Basic Auth.

**Default credentials (development only):**
- `frodo` / `ofTheNineFingers`
- `samwise` / `theStrong`

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/` | No | HTML homepage |
| `POST` | `/api/login` | Yes | Validate credentials |
| `POST` | `/api/shorten` | Yes | Create short URL |
| `GET` | `/api/urls` | Yes | List all URLs (max 10) |
| `GET` | `/api/stats` | Yes | Redirect statistics |
| `GET` | `/{shortCode}` | No | Redirect to original URL |
| `GET` | `/metrics` | No | Prometheus metrics |
| `GET` | `/debug/pprof/` | Yes | pprof index |

**Shorten a URL:**
```bash
curl -u frodo:ofTheNineFingers \
  -X POST http://localhost:8899/api/shorten \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/some/long/path"}'
```

---

## Observability

### Middleware Pipeline

```
Request
  └─ metricsMiddleware        (Prometheus counter)
      └─ requestIDMiddleware  (X-Request-ID header)
          └─ requestLogger    (structured access log)
              └─ otelhttp     (OpenTelemetry span)
                  └─ mux → handler
```

### Structured Log Fields

Every request log includes:

```json
{
  "method": "POST",
  "path": "/api/shorten",
  "client_ip": "192.168.1.x",
  "duration": "3.2ms",
  "request_body_bytes": 42,
  "response_status": 200,
  "response_body_bytes": 18,
  "request_id": "abc123",
  "user": "frodo",
  "git_sha": "8780782",
  "build_time": "2026-05-05T00:00:00Z",
  "env": "development",
  "hostname": "myhost"
}
```

**Auto-redacted fields:** `password`, `key`, `apikey`, `secret`, `pin`, `creditcardno`, `user`  
**Auto-redacted patterns:** IPv4 last octet (`192.168.1.x`), embedded URL passwords

### Log Rotation

Log files rotate at 1 MB, keep 10 backups, expire after 28 days, and are gzip-compressed.

```bash
LINKO_LOG_FILE=./linko.out.log ./linko
```

---

## Project Structure

```
linko-starter/
├── main.go               # Entry point: signal handling, logger init, tracing setup
├── server.go             # HTTP server, middleware stack, route registration
├── handlers.go           # Request handlers (index, login, shorten, redirect, stats)
├── auth.go               # Basic auth middleware, bcrypt validation
├── destination.go        # URL reachability validation
├── docker-compose.yaml   # Observability stack (Prometheus, Grafana, Jaeger, Node Exporter)
├── prometheus.yml        # Prometheus scrape config
├── index.html            # Terminal-style frontend
├── internal/
│   ├── store/            # File-based URL store (Create, Lookup, List)
│   ├── linkoerr/         # Structured error wrapping with slog attributes
│   ├── build/            # Build metadata injection (GitSHA, BuildTime)
│   └── spyResponse/      # Response instrumentation helpers
└── docs/
    ├── ARCHITECTURE.md   # System design and data flow
    ├── DEVOPS.md         # DevOps skills demonstrated and roadmap
    └── APP-TEMPLATE.md   # Reuse guide for new Go services
```

---

## Running Tests

```bash
go test ./...
```

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `LINKO_LOG_FILE` | (stderr only) | Path for JSON log file with rotation |
| `ENV` | `""` | Environment name; `production` disables `/admin/shutdown` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Jaeger OTLP gRPC endpoint |

