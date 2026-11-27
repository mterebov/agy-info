#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# GUI apps on macOS/Linux often inherit a minimal PATH without Go.
export PATH="/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:${HOME:-/tmp}/go/bin:${PATH:-}"

UNAME_S="$(uname -s)"
UNAME_M="$(uname -m)"
EXT=""

case "$UNAME_S" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  MINGW*|MSYS*|CYGWIN*) OS="windows"; EXT=".exe" ;;
  *)      OS="unknown" ;;
esac

case "$UNAME_M" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64|amd64)  ARCH="amd64" ;;
  *)             ARCH="unknown" ;;
esac

BIN_DIR="$PLUGIN_ROOT/bin"
BIN="$BIN_DIR/agy-info${EXT}"

if [ ! -x "$BIN" ]; then
  mkdir -p "$BIN_DIR" >&2
  TMP_BIN="${BIN}.tmp.$$"

  if command -v go >/dev/null 2>&1 && [ -f "$PLUGIN_ROOT/server/go.mod" ]; then
    echo "agy-info: building MCP server binary..." >&2
    if (cd "$PLUGIN_ROOT/server" && go build -trimpath -ldflags="-s -w" -o "$TMP_BIN" . 1>&2); then
      mv -f "$TMP_BIN" "$BIN" >&2
      chmod +x "$BIN" >&2 || true
    else
      rm -f "$TMP_BIN" >&2 || true
      echo "agy-info: failed to build server binary with Go." >&2
      exit 1
    fi
  elif [ -n "${AGY_INFO_GITHUB_REPO:=mterebov/agy-info}" ] && [ "$OS" != "unknown" ] && [ "$ARCH" != "unknown" ]; then
    ASSET="agy-info-${OS}-${ARCH}${EXT}"
    if [ -n "${AGY_INFO_VERSION:-}" ]; then
      URL="https://github.com/${AGY_INFO_GITHUB_REPO}/releases/download/${AGY_INFO_VERSION}/${ASSET}"
    else
      URL="https://github.com/${AGY_INFO_GITHUB_REPO}/releases/latest/download/${ASSET}"
    fi
    echo "agy-info: downloading prebuilt binary from ${URL}..." >&2
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL "$URL" -o "$TMP_BIN" 1>&2 || { rm -f "$TMP_BIN" >&2 || true; echo "agy-info: download failed." >&2; exit 1; }
    elif command -v wget >/dev/null 2>&1; then
      wget -qO "$TMP_BIN" "$URL" 1>&2 || { rm -f "$TMP_BIN" >&2 || true; echo "agy-info: download failed." >&2; exit 1; }
    else
      echo "agy-info: neither curl nor wget is available to download ${ASSET}." >&2
      exit 1
    fi
    mv -f "$TMP_BIN" "$BIN" >&2
    chmod +x "$BIN" >&2 || true
  else
    echo "agy-info: binary not found at ${BIN} and Go is not installed." >&2
    echo "agy-info: install Go 1.22+ (https://go.dev/dl/) or set AGY_INFO_GITHUB_REPO to download a release binary." >&2
    exit 1
  fi
fi

exec "$BIN" "$@"
