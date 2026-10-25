#!/usr/bin/env bash
# Install and run a published pingway binary. No Docker, no Go toolchain.
#
# From a checkout, the version comes from the VERSION file, so the binary
# matches the tree you are standing in:
#   ./install-binary.sh
#
# Without a checkout it installs the latest release:
#   curl -fsSL https://raw.githubusercontent.com/jasondcamp/pingway/main/install-binary.sh | bash
#
# Options via env:
#   VERSION=0.2.0        override the version to install
#   BIN_DIR=/usr/local/bin   where the binary lands
#   DATA_DIR=$HOME/pingway/data   SQLite lives here
#   LISTEN=:8080         address to serve on
#   NO_RUN=1             install only, do not start
#   OS=linux ARCH=arm64  fetch for another machine
#
# install.sh is the Docker equivalent of this, and is the better choice if
# you want restart-on-boot without writing a unit file yourself.
set -euo pipefail

REPO="${REPO:-jasondcamp/pingway}"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
DATA_DIR="${DATA_DIR:-$HOME/pingway/data}"
here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"

die() { echo "error: $*" >&2; exit 1; }

# --- version ---------------------------------------------------------------
# The VERSION file first, so running this inside a checkout installs the
# version that checkout describes rather than whatever is newest. Piping the
# script from the web has no checkout, so that falls through to the API.
if [[ -z "${VERSION:-}" && -f "$here/VERSION" ]]; then
  VERSION="$(tr -d '[:space:]' < "$here/VERSION")"
  echo "Version $VERSION (from $here/VERSION)"
elif [[ -z "${VERSION:-}" ]]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)"
  [[ -n "$VERSION" ]] || die "could not determine the latest release of $REPO"
  echo "Version $VERSION (latest release)"
else
  echo "Version $VERSION (from \$VERSION)"
fi

# --- platform --------------------------------------------------------------
OS="${OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
if [[ -z "${ARCH:-}" ]]; then
  case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "no published binary for $(uname -m); build from source with 'make build'" ;;
  esac
fi
if [[ "$OS" != "linux" ]]; then
  die "releases publish linux binaries only (this is $OS). Use ./install.sh to run it in Docker."
fi

tarball="pingway_${VERSION}_linux_${ARCH}.tar.gz"
base="https://github.com/$REPO/releases/download/v$VERSION"

# --- download and verify ---------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $tarball..."
curl -fsSL -o "$tmp/$tarball" "$base/$tarball" ||
  die "no asset $tarball in release v$VERSION of $REPO"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" ||
  die "release v$VERSION has no SHA256SUMS; refusing to install unverified"

# Checked rather than assumed: a truncated download is a binary that starts
# and then misbehaves, which is far worse than one that never installs.
if command -v sha256sum >/dev/null 2>&1; then
  sum="$(sha256sum "$tmp/$tarball" | awk '{print $1}')"
else
  sum="$(shasum -a 256 "$tmp/$tarball" | awk '{print $1}')"
fi
want="$(awk -v f="$tarball" '$2 == f || $2 == "*"f {print $1}' "$tmp/SHA256SUMS")"
[[ -n "$want" ]] || die "$tarball is not listed in SHA256SUMS"
[[ "$sum" == "$want" ]] || die "checksum mismatch for $tarball (got $sum, expected $want)"
echo "Checksum ok."

tar -xzf "$tmp/$tarball" -C "$tmp"
src="$tmp/pingway_${VERSION}_linux_${ARCH}/pingway"
[[ -x "$src" ]] || die "the tarball did not contain a pingway binary"

# --- install ---------------------------------------------------------------
mkdir -p "$DATA_DIR"
# Create BIN_DIR when it is somewhere we can create it. Without this, a
# BIN_DIR that does not exist yet fails the writability test below and
# escalates to sudo for no reason.
mkdir -p "$BIN_DIR" 2>/dev/null || true
if [[ -w "$BIN_DIR" ]]; then
  install -m 0755 "$src" "$BIN_DIR/pingway"
else
  echo "Installing to $BIN_DIR (may prompt for sudo)..."
  sudo install -m 0755 "$src" "$BIN_DIR/pingway"
fi
echo "Installed $BIN_DIR/pingway ($VERSION)"

# ICMP: the binary falls back to unprivileged UDP ping when it cannot open a
# raw socket, which works but is a different measurement. Say so once, here,
# rather than leaving it to be noticed in the logs later.
if [[ $EUID -ne 0 ]]; then
  range="$(cat /proc/sys/net/ipv4/ping_group_range 2>/dev/null || echo '1 0')"
  lo="${range%% *}"; hi="${range##* }"
  gid="$(id -g)"
  if ! { [[ "$lo" =~ ^[0-9]+$ && "$hi" =~ ^[0-9]+$ ]] && (( gid >= lo && gid <= hi )); }; then
    echo
    echo "Note: unprivileged ICMP is not available to your group, so pingway will"
    echo "  use UDP ping. For raw-socket ICMP, either:"
    echo "    sudo setcap cap_net_raw+ep $BIN_DIR/pingway"
    echo "  or widen the range:  sudo sysctl -w net.ipv4.ping_group_range='0 2147483647'"
  fi
fi

if [[ -n "${NO_RUN:-}" ]]; then
  echo
  echo "Not starting (NO_RUN set). To run it:"
  echo "  DATA_DIR=$DATA_DIR $BIN_DIR/pingway"
  exit 0
fi

echo
echo "Starting pingway on ${LISTEN:-:8080} with DATA_DIR=$DATA_DIR"
echo "  dashboard: http://localhost:8080   kiosk: http://localhost:8080/kiosk"
echo
exec env DATA_DIR="$DATA_DIR" ${LISTEN:+LISTEN="$LISTEN"} "$BIN_DIR/pingway"
