#!/usr/bin/env bash
# RidgeRiceTalk Server — Linux/macOS One-Click Launcher
# Usage: ./start.sh
# The terminal will stay open; closing it stops the server.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "========================================"
echo "  RidgeRiceTalk Server Starter"
echo "========================================"
echo ""

# Check if bash is available and executable
if [ ! -x "scripts/start-server.sh" ]; then
    chmod +x scripts/start-server.sh
fi

# Run the main start script
exec bash scripts/start-server.sh "$@"
