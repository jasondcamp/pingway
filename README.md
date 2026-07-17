# Pingway

**Continuous network path monitor.** One Docker container that pings your
LAN, gateway, ISP, and internet anchors every second, runs scheduled speed
tests, detects and logs outages, and serves a realtime dashboard plus a
kiosk display mode — so you can answer *"is my internet ok right now, and
where is the problem?"* at a glance, with history.

- **Path visualization** — [You] → LAN → ISP → Internet, with live RTT and
  per-hop health. If tier 1 is green and tier 3 is red, the problem is
  upstream, and the UI says so.
- **1-second ICMP monitoring** with explicit outage events (start, end,
  duration) — not just gaps in a chart.
- **Scheduled speed tests** (download, upload, idle + loaded latency) with
  pluggable engines: LibreSpeed (default), Cloudflare, Ookla.
- **Realtime dashboard** over SSE, uPlot history charts, tiered retention
  (raw 48h → 1-minute 30d → 1-hour forever) in a single SQLite file.
- **`/kiosk`** route for a dedicated always-on display (Raspberry Pi).
- Single static Go binary, frontend embedded. Multi-arch images
  (amd64/arm64). No external database. Zero config required.

## Quick start

```sh
mkdir -p data && sudo chown 65532:65532 data   # image runs as nonroot (uid 65532)

docker run -d --name pingway \
  --network host \
  --cap-add NET_RAW \
  -v "$PWD/data:/data" \
  --restart unless-stopped \
  ghcr.io/OWNER/pingway:latest
```

Open `http://<host>:8080`. With zero config, pingway auto-detects your
default gateway (tier 1) and monitors 1.1.1.1 and 8.8.8.8 (tier 3). Add
your own targets in **Settings**.

Or with compose: see [`docker-compose.yml`](docker-compose.yml).

### Networking notes

- `--network host` is recommended: pings originate from the host network
  namespace, so latency is true and LAN devices are reachable.
- **Bridge mode works too** (`-p 8080:8080` instead of `--network host`):
  every RTT gains a small constant from NAT traversal and the container
  can only reach LAN devices routable from the Docker bridge. Fine for
  "is the internet up", less precise for LAN diagnosis.
- **ICMP mode:** the image runs as a non-root user, so it uses
  unprivileged UDP ping by default (Docker enables this via
  `net.ipv4.ping_group_range`); results are equivalent for monitoring
  purposes. For privileged raw-socket ICMP, run with `--cap-add NET_RAW
  --user 0` — a non-root process cannot use the capability on its own.
  The active mode is logged at startup and shown in `/api/status`.

## Configuration

Everything is optional. Precedence: **UI edits (persisted to DB) > env
vars > YAML file > defaults**. YAML/env are read once at startup and
upserted; after that the DB is the source of truth — unless you set
`CONFIG_LOCK=true`, which makes env/yaml always win and disables UI edits.

### Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `TARGETS` | *(auto)* | `Name:host:tier,...` e.g. `Router:192.168.1.1:1,CF:1.1.1.1:3` |
| `LISTEN` | `:8080` | HTTP listen address |
| `DATA_DIR` | `/data` | SQLite + downloaded tools location |
| `PING_INTERVAL_MS` | `1000` | Ping cadence |
| `PING_TIMEOUT_MS` | `2000` | Ping timeout |
| `SPEEDTEST_ENGINE` | `librespeed` | `librespeed` \| `cloudflare` \| `ookla` |
| `SPEEDTEST_INTERVAL_MINUTES` | `45` | ±10% jitter applied |
| `SPEEDTEST_ENABLED` | `true` | |
| `OOKLA_ACCEPT_EULA` | `false` | Allow downloading the Ookla CLI (see below) |
| `CONFIG_LOCK` | `false` | env/yaml win; UI settings become read-only |
| `LOG_FORMAT` | *(auto)* | `json` or `text` |
| `CONFIG_FILE` | `/config/config.yaml` | YAML config path |
| `LIBRESPEED_SERVER` | *(auto)* | Pin a LibreSpeed backend base URL |

### YAML

Mount a config file at `/config/config.yaml` — see
[`config.example.yaml`](config.example.yaml) for all fields (targets,
tiers, thresholds, retention).

### Target tiers

- **Tier 1** — LAN infrastructure: router, mesh hub, security appliance.
- **Tier 2** — ISP first hop / CMTS gateway (find it with `traceroute 1.1.1.1`, first non-private hop). Prefer this over pinging your own
  public IP: from inside the LAN that ping usually hairpins at your
  router's WAN interface and never traverses the ISP, so it can look
  healthy while your line is down.
- **Tier 3** — internet anchors (1.1.1.1, 8.8.8.8, …). An **internet
  outage** is recorded when *all* tier 3 targets are down simultaneously.

## Speed test engines

| Engine | Notes |
|---|---|
| `librespeed` | Default. Native implementation against public LibreSpeed servers; fully open. Pin your own server with `LIBRESPEED_SERVER`. |
| `cloudflare` | Uses speed.cloudflare.com `__down`/`__up` endpoints. **Not an official API** — may break or rate-limit at any time; failures are recorded, the scheduler keeps running. |
| `ookla` | Executes the official `speedtest` CLI. Not bundled (EULA). Set `OOKLA_ACCEPT_EULA=true` and pingway downloads it to the data volume on first use — doing so accepts [Ookla's EULA](https://www.speedtest.net/about/eula). |

Throughput is measured honestly: fixed 10s transfers, first 2s ramp-up
discarded, computed from bytes moved in the steady-state window. While a
test runs, concurrent ping samples are flagged `during_speedtest`, and
**loaded latency** (bufferbloat) is derived from them when the engine
doesn't provide its own.

Failed runs are stored with their error string — they're data, not noise.
Scheduled runs are skipped (and marked `skipped_outage`) while an
internet-level outage is active.

## HTTP API

```
GET  /api/status            live per-target state, loss, ping mode, outages
GET  /api/targets           CRUD (POST /api/targets, PUT/DELETE /api/targets/{id})
GET  /api/ping?target=&from=&to=&resolution=raw|1m|1h   (auto-selects by range)
GET  /api/speedtests?from=&to=
POST /api/speedtest/run     manual trigger (409 while one is running)
GET  /api/outages?from=&to=&target=
GET  /api/summary?range=1h|6h|24h|7d|30d
GET  /api/stream            SSE: ping (1s batches), status, speedtest
GET  /api/export?format=csv&table=ping_samples|ping_rollup_1m|ping_rollup_1h|speedtest_results|outage_events|targets
GET  /healthz               liveness (pinger goroutines + DB writable)
```

All timestamps are unix milliseconds UTC.

## Security

Pingway ships **without auth** and assumes a trusted LAN. To expose it
further, put it behind a reverse proxy that terminates auth, e.g. Caddy:

```
pingway.example.com {
    basic_auth {
        me $2a$14$...hash...
    }
    reverse_proxy 127.0.0.1:8080
}
```

(or Authelia/OAuth2-proxy/Tailscale Serve — anything that fronts HTTP).

## Raspberry Pi

Works well on a Pi 4/5 with the 64-bit OS (arm64 image; RSS well under
100MB). **Use ethernet** if you can: on WiFi, speed tests measure the Pi's
radio, not your line.

### Kiosk display recipe

1. Run the container on the Pi (or anywhere on the LAN).
2. Run the copy-paste script:

```sh
curl -fsSL https://raw.githubusercontent.com/OWNER/pingway/main/contrib/pi-kiosk-setup.sh | bash -s -- http://localhost:8080/kiosk
```

It installs Chromium if needed, disables screen blanking, and installs a
systemd user unit that launches Chromium in kiosk mode on boot (Wayland
labwc/wayfire and X11 both work). Query params: `?theme=dark|light`,
`?scale=1.2`.

The kiosk shows the path visualization, live latency and loss for your
primary internet anchor, and the last speed test. If the pingway backend
becomes unreachable, the display switches to an unmissable **MONITOR
OFFLINE** state so a dead monitor is never mistaken for a dead internet
connection.

## Development

```sh
make frontend   # npm install + vite build (once, and after frontend changes)
make run        # go build + run with ./data, text logs
make test       # go vet + go test -race ./...
make docker     # local image build
```

The frontend is plain TypeScript + [uPlot](https://github.com/leeoniya/uPlot),
built with Vite and embedded into the Go binary via `embed.FS`. Backend is
Go with pure-Go SQLite (`modernc.org/sqlite`, WAL mode) — no CGO anywhere,
so cross-compilation is trivial.

Run the LibreSpeed integration test (hits real servers):

```sh
go test -tags=integration -run TestLibreSpeedIntegration ./internal/speedtest -v
```

### Architecture

One binary, a handful of supervised goroutines (panics are recovered and
restarted with backoff — a silently dead pinger is the worst failure mode
a monitor can have):

- **Pinger** — one drift-free 1s loop per target (injected ping function;
  pro-bing in production). Samples fan out to the writer, outage detector,
  and SSE hub.
- **Writer** — the *only* goroutine that writes ping samples; batches
  inserts into one transaction every ~1.5s (your Pi's SD card thanks you).
- **OutageDetector** — pure per-target state machine: N consecutive
  failures opens an outage event, M consecutive successes closes it.
  Open events survive restarts.
- **Aggregator** — every 5 min: incremental 1m/1h rollups (p95, jitter)
  with cursors in `settings`, then retention pruning.
- **SpeedTester** — jittered scheduler + engine interface; manual trigger
  via the API.
- **SSE hub** — per-client buffered channels; a slow client is dropped,
  never blocks the broadcaster.

## License

See [LICENSE](LICENSE).
