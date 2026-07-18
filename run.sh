#!/usr/bin/env bash
# Build (if needed) and run pingway in Docker, taking care of the data dir.
#
# Usage:
#   ./run.sh              # build local image if missing, run it
#   ./run.sh --build      # force rebuild of the image
#   IMAGE=ghcr.io/OWNER/pingway:latest ./run.sh   # run a published image
#   PORT=9090 ./run.sh    # bridge-mode port (non-Linux only)
set -euo pipefail

cd "$(dirname "$0")"

IMAGE="${IMAGE:-pingway:dev}"
NAME="${NAME:-pingway}"
PORT="${PORT:-8080}"
DATA_DIR="${DATA_DIR:-$PWD/data}"

if ! docker info >/dev/null 2>&1; then
  echo "error: docker daemon is not running" >&2
  exit 1
fi

# --- image ---
if [[ "${1:-}" == "--build" ]] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  if [[ "$IMAGE" == ghcr.io/* ]]; then
    docker pull "$IMAGE"
  else
    echo "Building $IMAGE..."
    docker build -t "$IMAGE" .
  fi
fi

# --- data dir (image runs as distroless nonroot, uid/gid 65532) ---
mkdir -p "$DATA_DIR"
if [[ "$(uname -s)" == "Linux" ]]; then
  owner="$(stat -c '%u' "$DATA_DIR")"
  if [[ "$owner" != "65532" ]]; then
    echo "Fixing ownership of $DATA_DIR (needs uid 65532, may prompt for sudo)..."
    sudo chown 65532:65532 "$DATA_DIR"
  fi
fi
# on macOS/Windows, Docker Desktop's file sharing handles permissions

# --- run ---
docker rm -f "$NAME" >/dev/null 2>&1 || true

args=(-d --name "$NAME" --restart unless-stopped -v "$DATA_DIR:/data")
if [[ -f "$PWD/.env" ]]; then
  echo "Using env file: $PWD/.env"
  args+=(--env-file "$PWD/.env")
fi
if [[ "$(uname -s)" == "Linux" ]]; then
  # host networking: true latency + LAN reachability
  args+=(--network host --cap-add NET_RAW)
  url="http://localhost:8080"
else
  # Docker Desktop has no real host networking; use bridge + published port
  args+=(-p "$PORT:8080")
  url="http://localhost:$PORT"
fi

docker run "${args[@]}" "$IMAGE" >/dev/null
echo "pingway is starting: $url  (kiosk: $url/kiosk)"
echo "logs:  docker logs -f $NAME"
echo "stop:  docker rm -f $NAME"
