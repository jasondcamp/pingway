# Pingway: Continuous Network Path Monitor

Project URL: pingway.net. Binary name `pingway`, image `ghcr.io/OWNER/pingway`. Style the name "Pingway" in prose, lowercase `pingway` everywhere technical.

## One-liner

A single Docker container that continuously monitors the full network path (LAN devices, gateway, ISP, internet), runs scheduled speed tests, detects and logs outages, and serves a realtime dashboard plus a kiosk display mode. Designed to answer "is my internet ok right now, and where is the problem" at a glance, with history.

## Goals

- One `docker run` deployment. No external database, no config required to start (sane defaults), everything optional.
- Continuous ICMP monitoring (1s interval) of an ordered chain of targets: local devices -> gateway -> ISP -> internet.
- Automatic fault localization: if tier 1 targets are healthy but tier 3 is dropping, the problem is upstream, and the UI says so.
- Scheduled speed tests (download, upload, latency under load) with pluggable engines.
- Explicit downtime/outage event tracking per target (start, end, duration), not just gaps in data.
- Realtime dashboard (SSE) plus historical charts with tiered data retention.
- `/kiosk` route optimized for a dedicated always-on display (Raspberry Pi + monitor).
- Multi-arch images: linux/amd64 and linux/arm64.

## Non-goals (v1)

- No per-device LAN discovery, SNMP, router API integration, or bandwidth-per-client accounting.
- No multi-node/agent architecture. One container, one vantage point.
- No auth (assume trusted LAN; document reverse-proxy auth as the pattern).
- No alerting/notifications (design the events table so this is easy to add in v2).

## Stack

- **Backend:** Go 1.22+. Single static binary. Frontend embedded via `embed.FS`.
- **ICMP:** `github.com/prometheus-community/pro-bing` (supports privileged raw sockets and unprivileged UDP fallback).
- **Storage:** SQLite via `modernc.org/sqlite` (pure Go, no CGO, trivial cross-compilation). WAL mode.
- **Frontend:** Vanilla or lightweight framework (Preact or plain TS acceptable), built with Vite, output embedded. Charts: **uPlot** (must handle 100K+ points smoothly). No heavy component libraries.
- **Realtime:** Server-Sent Events. No websockets.
- **Container:** Multi-stage build, final stage distroless or scratch + ca-certificates + tzdata. Target image size under 30MB.

## Architecture

```
+------------------------------------------------------+
|  Go binary                                           |
|                                                      |
|  Pinger (goroutine per target, drift-free 1s ticks)  |
|     -> sample channel -> Writer (batched inserts,    |
|        sole DB writer for samples)                   |
|     -> fan-out to OutageDetector and SSE hub         |
|                                                      |
|  SpeedTester (scheduler, jittered interval)          |
|     -> engine interface (librespeed | cloudflare |   |
|        ookla)                                        |
|                                                      |
|  Aggregator (rollups + retention pruning, 5 min)     |
|                                                      |
|  OutageDetector (state machine per target)           |
|                                                      |
|  HTTP server                                         |
|     /            dashboard (embedded SPA)            |
|     /kiosk       display mode                        |
|     /api/*       REST                                 |
|     /api/stream  SSE (broadcast hub, per-client      |
|                  buffered channels, slow-client      |
|                  eviction)                           |
|                                                      |
|  SQLite (WAL) on mounted volume                      |
+------------------------------------------------------+
```

Concurrency rules:
- **Single-writer pattern:** exactly one goroutine writes ping samples. All samples flow through a buffered channel into the Writer, which flushes batched transactions every 1-2 seconds. Never one transaction per sample (SD card longevity on the Pi depends on this).
- Ping loops must be drift-free: schedule against monotonic deadlines (`next = next.Add(interval)`), not sleep-after-work. `time.Ticker` is acceptable.
- SSE hub: per-client buffered channel; if a client can't keep up, drop the client, never block the broadcaster.
- All long-lived goroutines run under a supervisor that recovers panics, logs, and restarts the goroutine with backoff. A silently dead pinger goroutine is the worst failure mode this app can have.
- Graceful shutdown: context cancellation from main, flush pending writes, leave open outage events open (they resume as open on restart or get closed by a startup reconciliation pass).

## Target model

Targets are first-class, ordered, tiered.

```yaml
# /config/config.yaml (all fields optional except name+host)
listen: ":8080"
speedtest:
  engine: librespeed        # librespeed | cloudflare | ookla
  interval_minutes: 45      # jitter of +/-10% applied
  enabled: true
ping:
  interval_ms: 1000
  timeout_ms: 2000
targets:
  - name: Firewalla
    host: 192.168.1.1
    tier: 1
  - name: Deco Hub
    host: 192.168.1.2
    tier: 1
  - name: ISP Gateway
    host: 96.120.0.1
    tier: 2
  - name: Cloudflare
    host: 1.1.1.1
    tier: 3
  - name: Google DNS
    host: 8.8.8.8
    tier: 3
```

Tier semantics (used for fault localization and path visualization):
- **Tier 1:** LAN infrastructure (router, mesh hub, security appliance)
- **Tier 2:** ISP first hop / gateway
- **Tier 3:** Internet anchors

Config precedence: UI edits (persisted to DB) > env vars > YAML file > defaults.

Env var fallback for lazy setup:
```
TARGETS="Firewalla:192.168.1.1:1,Deco:192.168.1.2:1,Cloudflare:1.1.1.1:3"
SPEEDTEST_INTERVAL_MINUTES=45
SPEEDTEST_ENGINE=librespeed
```

Default targets if nothing configured: auto-detect default gateway (parse `/proc/net/route`, works in-container) as tier 1, plus 1.1.1.1 and 8.8.8.8 (tier 3). The app must start and be useful with zero config.

Targets are editable in the UI (add/remove/rename/reorder/enable-disable) and persist to the `targets` table. YAML/env are read once at startup and upserted; DB is source of truth thereafter unless `CONFIG_LOCK=true`. Target changes at runtime start/stop pinger goroutines without restart.

## Pinger

- One goroutine per enabled target. 1s cadence (configurable), drift-free. ICMP echo with 2s timeout.
- Attempt privileged raw socket first (requires CAP_NET_RAW); fall back to unprivileged UDP ping automatically and log which mode is active. Expose mode in `/api/status`.
- Each sample: `target_id, ts (unix ms), rtt_us (nullable), success (bool), during_speedtest (bool)`.
- Jitter is computed at read/rollup time, not stored per sample.
- Fan-out: each sample goes to the Writer channel, the OutageDetector, and the SSE hub.

## Speed tests

Engine interface:

```go
type SpeedTestEngine interface {
    Name() string
    Run(ctx context.Context) (*SpeedTestResult, error)
}

type SpeedTestResult struct {
    Engine          string
    ServerName      string
    ServerID        string
    DownloadBps     float64
    UploadBps       float64
    LatencyMs       float64   // idle latency
    LoadedLatencyMs float64   // latency under load, derived or engine-provided
    PacketLoss      float64   // if engine provides it
    RanAt           time.Time
    DurationMs      int64
    Error           string    // non-empty if failed
}
```

Engines, in implementation priority order:
1. **librespeed:** implement natively with net/http against public LibreSpeed servers (garbage download endpoints, chunked upload, measure throughput). Default engine. Fully open, no EULA issues.
2. **cloudflare:** implement against speed.cloudflare.com endpoints (`__down?bytes=`, `__up`). No official API; document fragility, degrade gracefully.
3. **ookla:** exec the official `speedtest` CLI with `--format=json --accept-license --accept-gdpr`. Do NOT bundle the binary in the image. Provide an env flag `OOKLA_ACCEPT_EULA=true` that downloads the CLI to the data volume on first use, with a clear log line about the Ookla EULA. If the flag is unset and engine=ookla, fail with a helpful error.

Scheduler: run at `interval_minutes` with +/-10% random jitter. Also support manual trigger via `POST /api/speedtest/run` (return 409 if one is already running; guard with a mutex/atomic). Skip scheduled runs while an outage is active on all tier 3 targets, and record a `skipped_outage` marker instead of a failed result.

Failed speed tests are stored with the error string. They are data, not noise.

Measurement honesty: run transfers for a fixed duration (e.g. 10s down, 10s up), compute throughput from bytes moved in the steady-state window (discard first 2s ramp-up). While a speed test is running, set the `during_speedtest` flag on concurrent ping samples so latency-under-load can be derived and rollups can optionally exclude self-inflicted latency spikes.

## Outage detection

Per-target state machine over the ping stream:

- **UP -> DOWN:** N consecutive failures (default 5) transitions to DOWN and opens an outage event (`started_at` = timestamp of first failure in the run).
- **DOWN -> UP:** M consecutive successes (default 3) closes the event (`ended_at` = timestamp of first success in the run).
- Events: `id, target_id, started_at, ended_at (nullable while open), duration_ms (computed on close)`.
- A derived "internet outage" is when ALL tier 3 targets are simultaneously DOWN. Compute at read time or maintain as a synthetic event; either is fine, but the API must expose internet-level uptime % and outage log distinct from per-target.
- Pure function core, no I/O: `Feed(sample) -> transition or nil`, fully unit-testable.

## Storage and retention

SQLite, WAL mode, `synchronous=NORMAL`. Single file on the mounted volume at `/data/pingway.db`. Schema migrations: numbered embedded SQL files applied at startup, version tracked in `settings`.

Tables:

```sql
targets(id, name, host, tier, sort_order, enabled, created_at)

ping_samples(target_id, ts, rtt_us, success, during_speedtest)
  -- index (target_id, ts)

ping_rollup_1m(target_id, ts_bucket, sent, lost, rtt_avg_us,
               rtt_min_us, rtt_max_us, rtt_p95_us, jitter_us)

ping_rollup_1h(same shape as 1m)

speedtest_results(id, engine, server_name, server_id, download_bps,
                  upload_bps, latency_ms, loaded_latency_ms,
                  packet_loss, ran_at, duration_ms, error)

outage_events(id, target_id, started_at, ended_at, duration_ms)

settings(key, value)  -- UI-edited config, schema version, rollup cursors
```

Retention (Aggregator runs every 5 min):
- Raw `ping_samples`: 48 hours, then delete (after rollup).
- `ping_rollup_1m`: 30 days.
- `ping_rollup_1h`: forever.
- `speedtest_results` and `outage_events`: forever.

Rollups computed incrementally (track last rolled-up bucket per target in `settings`), never full-table rescans. p95 computed from raw samples during rollup. Jitter = mean absolute difference of consecutive RTTs in the bucket.

Expected volume check: 5 targets x 86,400 samples/day = 432K rows/day raw, bounded at ~900K rows by 48h retention. Trivial for SQLite with proper indexes and batched writes.

## HTTP API

```
GET  /api/status              current state: per-target UP/DOWN, live latency,
                              loss (rolling 60s), ping mode, active outages,
                              last speedtest, app version
GET  /api/targets             list
POST /api/targets             create
PUT  /api/targets/:id         update
DELETE /api/targets/:id       delete (keeps historical data)
GET  /api/ping?target=&from=&to=&resolution=raw|1m|1h
                              auto-select resolution if omitted based on range
GET  /api/speedtests?from=&to=
POST /api/speedtest/run       manual trigger
GET  /api/outages?from=&to=&target=
GET  /api/summary?range=24h|7d|30d
                              uptime %, avg/p95 latency per target,
                              speed test min/avg/max, outage count+total duration
GET  /api/stream              SSE
GET  /api/export?format=csv&table=...   raw data export (streamed)
GET  /healthz                 liveness (checks pinger goroutines alive + DB writable)
```

SSE events (JSON payloads):
- `ping`: batch of latest samples per target, emitted every 1s
- `status`: emitted on any state transition (target up/down, speedtest started/finished)
- `speedtest`: result on completion

## Frontend

### Dashboard `/`

Top section, "right now":
- **Path visualization:** horizontal chain of nodes: [You] -> tier 1 targets -> tier 2 -> tier 3 -> [Internet]. Each node shows name, live RTT, and status color (green: <2% loss over 60s; yellow: 2-10% loss or RTT >3x its 24h baseline; red: DOWN). Links between nodes colored by downstream health. This is the hero element.
- Live latency sparkline (last 5 min, all targets overlaid, per-target toggle).
- Rolling 60s packet loss per target.
- Last speed test: down/up/latency, time ago, engine, plus a "Run now" button.
- If an internet-level outage is active: prominent red banner with elapsed duration.

Bottom section, history:
- Time range picker: 1h / 6h / 24h / 7d / 30d / custom.
- Latency chart (uPlot, band or line per target, loss overlaid as markers or secondary axis).
- Speed test chart (down/up over time).
- Outage log table: target, start, end, duration, sortable/filterable.
- Summary stat cards for the selected range (uptime %, p95 latency, avg speeds).

Settings page: target CRUD, speed test engine + interval, retention knobs, Ookla EULA toggle.

### Kiosk `/kiosk`

- Dark theme, no chrome, no interactive elements, fills viewport at 800x480 through 1920x1080.
- Contents: path visualization (large), current down/up from last test, live latency big-number for the primary tier 3 target, loss %, outage banner if active, small 1h latency sparkline.
- Auto-reconnecting SSE. If the backend is unreachable, show an unmissable "MONITOR OFFLINE" state (the display itself failing must be distinguishable from the internet failing).
- Query params: `?theme=dark|light&scale=1.0`.

Design language: minimal, high contrast, tabular numerals for all metrics, no gradients or decoration. Status colors must be readable at 3 meters.

## Container and deployment

Dockerfile: multi-stage. Stage 1 (`node:22-slim`) builds the frontend with Vite; stage 2 `go build` with `CGO_ENABLED=0`, embedding the frontend; final stage distroless/static or scratch + ca-certificates + tzdata. Non-root user.

```yaml
# docker-compose.yml (reference)
services:
  pingway:
    image: ghcr.io/OWNER/pingway:latest
    network_mode: host          # recommended: true latency, LAN reachability
    cap_add:
      - NET_RAW                 # privileged ICMP; omit to use UDP fallback
    volumes:
      - ./data:/data
      - ./config.yaml:/config/config.yaml:ro   # optional
    environment:
      - TZ=America/New_York
    restart: unless-stopped
```

- Must also work in bridge mode with a published port (document the latency caveat).
- `GOMEMLIMIT` set sensibly; total RSS target under 100MB on a Pi 4.
- GitHub Actions: buildx multi-arch (amd64, arm64), push to GHCR on tag. CI job: `go vet`, `golangci-lint`, tests, race-detector pass on the pinger/writer.

## Raspberry Pi notes (include in README)

- Works on Pi 4/5, 64-bit OS. arm64 image.
- Recommend ethernet connection so speed tests measure the line, not the Pi's WiFi.
- Kiosk recipe: Chromium in kiosk mode pointed at `http://localhost:8080/kiosk`, autostart via systemd user unit or labwc/wayfire autostart. Provide a copy-paste script `contrib/pi-kiosk-setup.sh`.

## Testing requirements

- Unit tests: outage state machine (all transition edges, flapping), rollup math (p95, jitter), retention pruning, config precedence, TARGETS env-var parsing.
- Pinger/Writer: the pinger takes an injected ping function (pro-bing is the production implementation); simulated target that fails on schedule tests outage detection end to end against a temp SQLite file. Race detector clean.
- Drift test: assert the pinger's scheduled timestamps stay aligned to the interval over simulated time.
- Speed test engines behind the interface get a mock; librespeed engine gets one integration test behind a build tag, skipped in CI by default.
- API: golden tests for `/api/summary` and `/api/ping` resolution auto-selection.
- SSE: test that a slow client is evicted without blocking the hub.

## Milestones

1. **Core loop:** config load, pinger, SQLite writer, outage detector, `/api/status`, `/healthz`. Logs prove it works headless.
2. **History:** rollups, retention, ping/summary/outage APIs.
3. **Speed tests:** engine interface, librespeed engine, scheduler, manual trigger.
4. **Dashboard:** SSE, path visualization, live section, history charts, settings CRUD.
5. **Kiosk mode.**
6. **Packaging:** multi-arch images, compose file, README with Pi recipe, cloudflare + ookla engines.

Each milestone should be a working, runnable state.

## Conventions

- Layout: `cmd/pingway/main.go`, `internal/{pinger,speedtest,store,outage,api,sse,config}`, `frontend/`, `migrations/`, `contrib/`.
- All timestamps stored as unix milliseconds UTC; frontend localizes.
- Structured logging (slog), JSON in production, text in dev (`LOG_FORMAT` env).
- No global state; dependencies injected via constructors.
- Errors wrapped with context; no panics outside main (and supervisor-recovered in goroutines).
