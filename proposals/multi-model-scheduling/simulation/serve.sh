#!/usr/bin/env bash
# Serve the simulation locally and open it in the default browser.
#
# Usage:  ./serve.sh [PORT]
# Default port: 8765. Pass another to override (e.g. ./serve.sh 9000).
set -euo pipefail

PORT="${1:-8765}"
DIR="$(cd "$(dirname "$0")" && pwd)"
URL="http://localhost:${PORT}/index.html"

cd "$DIR"

if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required but not installed."
    echo "Alternatively, since the simulation is self-contained, you can just open"
    echo "  $DIR/index.html"
    echo "directly in a browser (file:// URL)."
    exit 1
fi

# Pick an opener
if command -v open >/dev/null 2>&1; then
    OPENER=open      # macOS
elif command -v xdg-open >/dev/null 2>&1; then
    OPENER=xdg-open  # Linux
else
    OPENER=""
fi

echo "Serving $DIR at $URL"
echo "Ctrl-C to stop."
echo ""

# Open the browser after a short delay so the server is up
if [ -n "$OPENER" ]; then
    ( sleep 0.8 && "$OPENER" "$URL" ) &
fi

# Bind only to loopback — this is a local dev server, not a public endpoint
exec python3 -m http.server "$PORT" --bind 127.0.0.1
