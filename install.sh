#!/usr/bin/env bash
# Standalone pingway runner — no repo checkout needed. Pulls the published
# image and runs it with sane platform defaults.
#
# Quick start (any machine with docker):
#   curl -fsSL https://raw.githubusercontent.com/jasondcamp/pingway/main/install.sh | bash
#
# Options via env:
#   IMAGE=ghcr.io/jasondcamp/pingway:latest   image to run
#   NAME=pingway                              container name
#   DATA_DIR=$HOME/pingway/data               SQLite volume
#   PORT=8080                                 published port (non-Linux only)
#   TZ, TARGETS, SPEEDTEST_ENGINE, SPEEDTEST_INTERVAL_MINUTES  passed through
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/jasondcamp/pingway:latest}"
NAME="${NAME:-pingway}"
DATA_DIR="${DATA_DIR:-$HOME/pingway/data}"
PORT="${PORT:-8080}"
ENV_FILE="${ENV_FILE:-$PWD/.env}"

if ! command -v docker >/dev/null 2>&1; then
  echo "error: docker is not installed" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "error: docker daemon is not running (or you need sudo/docker group)" >&2
  exit 1
fi

echo "Pulling $IMAGE..."
docker pull "$IMAGE"

# data dir: the image runs as distroless nonroot (uid/gid 65532)
mkdir -p "$DATA_DIR"
if [[ "$(uname -s)" == "Linux" ]]; then
  owner="$(stat -c '%u' "$DATA_DIR")"
  if [[ "$owner" != "65532" ]]; then
    echo "Fixing ownership of $DATA_DIR (uid 65532; may prompt for sudo)..."
    sudo chown 65532:65532 "$DATA_DIR"
  fi
fi

docker rm -f "$NAME" >/dev/null 2>&1 || true

args=(-d --name "$NAME" --restart unless-stopped -v "$DATA_DIR:/data")
if [[ -f "$ENV_FILE" ]]; then
  echo "Using env file: $ENV_FILE"
  args+=(--env-file "$ENV_FILE")
fi
# explicit env vars win over the .env file
for var in TZ TARGETS SPEEDTEST_ENGINE SPEEDTEST_INTERVAL_MINUTES OOKLA_ACCEPT_EULA CONFIG_LOCK; do
  if [[ -n "${!var:-}" ]]; then
    args+=(-e "$var=${!var}")
  fi
done

if [[ "$(uname -s)" == "Linux" ]]; then
  # host networking: true latency + LAN reachability; NET_RAW enables
  # privileged ICMP for root (nonroot image falls back to UDP ping)
  args+=(--network host --cap-add NET_RAW)
  url="http://localhost:8080"
else
  # Docker Desktop (macOS/Windows): no host networking; bridge + port
  args+=(-p "$PORT:8080")
  url="http://localhost:$PORT"
fi

docker run "${args[@]}" "$IMAGE" >/dev/null
echo
echo "pingway is starting: $url   (kiosk: $url/kiosk)"
echo "  logs:    docker logs -f $NAME"
echo "  stop:    docker rm -f $NAME"
echo "  upgrade: re-run this script (data survives in $DATA_DIR)"
