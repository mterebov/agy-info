#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

export PATH="/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:${HOME:-/tmp}/go/bin:${PATH:-}"

PLUGINS_DIR="${AGY_PLUGINS_DIR:-$HOME/.gemini/config/plugins}"
MODE="copy"

usage() {
  cat <<'EOF'
Usage: ./scripts/install.sh [options]

Install the AGY Info plugin into Antigravity's plugins directory.

Options:
  --symlink         Symlink this repository into the plugins directory (for development)
  --dest <dir>      Override plugins root directory (default: ~/.gemini/config/plugins)
  -h, --help        Show this help message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --symlink)
      MODE="symlink"
      shift
      ;;
    --dest)
      if [ $# -lt 2 ]; then
        echo "Error: --dest requires a directory path." >&2
        exit 1
      fi
      PLUGINS_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Error: unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

DEST_DIR="$PLUGINS_DIR/agy-info"
mkdir -p "$PLUGINS_DIR"

same_dir() {
  [ -d "$1" ] && [ -d "$2" ] && [ "$(cd "$1" && pwd -P)" = "$(cd "$2" && pwd -P)" ]
}

if same_dir "$SRC_ROOT" "$DEST_DIR"; then
  echo "Plugin is already located at ${DEST_DIR}; skipping copy."
elif [ "$MODE" = "symlink" ]; then
  if [ -L "$DEST_DIR" ]; then
    rm -f "$DEST_DIR"
  elif [ -e "$DEST_DIR" ]; then
    echo "Error: ${DEST_DIR} already exists and is not a symlink. Remove it first or install without --symlink." >&2
    exit 1
  fi
  ln -s "$SRC_ROOT" "$DEST_DIR"
  echo "Linked ${DEST_DIR} -> ${SRC_ROOT}"
else
  if [ -L "$DEST_DIR" ]; then
    rm -f "$DEST_DIR"
  fi
  mkdir -p "$DEST_DIR"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete \
      --exclude='.git/' \
      --exclude='bin/' \
      --exclude='dist/' \
      --exclude='ui/node_modules/' \
      --exclude='.DS_Store' \
      --exclude='.env' \
      --exclude='.env.*' \
      --exclude='*.bak*' \
      "$SRC_ROOT/" "$DEST_DIR/"
  else
    # Portable fallback when rsync is unavailable.
    rm -rf "$DEST_DIR"
    mkdir -p "$DEST_DIR"
    (
      cd "$SRC_ROOT"
      tar --exclude='.git' \
          --exclude='bin' \
          --exclude='dist' \
          --exclude='ui/node_modules' \
          --exclude='.DS_Store' \
          --exclude='.env' \
          --exclude='.env.*' \
          --exclude='*.bak*' \
          -cf - .
    ) | (cd "$DEST_DIR" && tar -xf -)
  fi
  echo "Copied plugin files to ${DEST_DIR}"
fi

chmod +x "$DEST_DIR/scripts/run.sh" "$DEST_DIR/scripts/install.sh" 2>/dev/null || true

UNAME_S="$(uname -s)"
EXT=""
case "$UNAME_S" in
  MINGW*|MSYS*|CYGWIN*) EXT=".exe" ;;
esac
BIN="$DEST_DIR/bin/agy-info${EXT}"

if command -v go >/dev/null 2>&1 && [ -f "$DEST_DIR/server/go.mod" ]; then
  echo "Building Go MCP server..."
  mkdir -p "$DEST_DIR/bin"
  (cd "$DEST_DIR/server" && go build -trimpath -ldflags="-s -w" -o "$BIN" .)
  chmod +x "$BIN" 2>/dev/null || true
  echo "Built ${BIN}"
elif [ -x "$BIN" ]; then
  echo "Using existing binary at ${BIN}"
else
  echo "Warning: Go was not found in PATH and ${BIN} does not exist yet." >&2
  echo "Install Go 1.22+ (https://go.dev/dl/) or place a prebuilt binary at ${BIN}." >&2
fi

if [ -f "$DEST_DIR/assets/app.html" ]; then
  echo "Verified UI bundle: ${DEST_DIR}/assets/app.html"
else
  echo "Warning: ${DEST_DIR}/assets/app.html is missing." >&2
  echo "Run 'make ui' (requires Node.js 20+) to build the single-file UI bundle." >&2
fi

cat <<EOF

AGY Info is installed at: ${DEST_DIR}

Next steps in Antigravity:
  1. Enable "AGY Info" in Plugins Manager (or reload Antigravity if already running).
  2. Open AGY Info from the '+' button in the side pane, or from the left sidebar.
  3. You can also ask the agent: "Open AGY Info for the current session".
EOF
