#!/usr/bin/env bash
# Run a pingway call-probe reflector in Docker. Put this on a host
# OUTSIDE the network you monitor (a cheap VPS). See docs/call-probe.md.
#
# Usage:
#   ./reflector.sh                 # run ghcr image on UDP 15000
#   PORT=16000 ./reflector.sh      # different published port
#   MAX_PPS=50000 ./reflector.sh   # raise the global echo cap
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/jasondcamp/pingway-reflector:latest}"
NAME="${NAME:-pingway-reflector}"
PORT="${PORT:-15000}"
MAX_PPS="${MAX_PPS:-20000}"
PER_IP_PPS="${PER_IP_PPS:-120}"

if ! docker info >/dev/null 2>&1; then
  echo "error: docker daemon is not running" >&2
  exit 1
fi

docker pull "$IMAGE"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --restart unless-stopped \
  -p "$PORT:15000/udp" \
  "$IMAGE" -max-pps "$MAX_PPS" -per-ip-pps "$PER_IP_PPS"

echo "reflector is up on UDP $PORT"
echo "point pingway at it:  CALLPROBE_REFLECTORS=MyReflector:<this host's IP>:$PORT"
echo "logs:  docker logs -f $NAME"
echo "stop:  docker rm -f $NAME"
