#!/bin/bash
# RidgeRiceTalk Server Setup Script (Linux/macOS)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

STORAGE_DIR="./storage"
DB_FILE="$STORAGE_DIR/ridgericetalk.db"

# Ensure storage directory exists
mkdir -p "$STORAGE_DIR"
echo "✓ Storage directory ready: $STORAGE_DIR"

# Copy frontend dist if exists
# Note: setup.sh is located at ridgericetalk/server/scripts/ and switches to
# ridgericetalk/server/ above. The canonical frontend sources now live under
# the same repository at ../web/ (i.e. ridgericetalk/web/).
if [ -d "../web/voice/dist" ]; then
    mkdir -p ./webhost/dist/voice
    cp -r ../web/voice/dist/* ./webhost/dist/voice/
    echo "✓ Copied voice frontend dist"
fi

if [ -d "../web/admin/dist" ]; then
    mkdir -p ./webhost/dist/admin
    cp -r ../web/admin/dist/* ./webhost/dist/admin/
    echo "✓ Copied admin frontend dist"
fi

# Generate JWT secret if not set
if [ -z "$RRT_JWT_SECRET" ]; then
    export RRT_JWT_SECRET="dev-jwt-secret-$(date +%s)"
    echo "⚠ Generated development JWT secret"
fi

if [ -z "$RRT_CSRF_TOKEN_SECRET" ]; then
    export RRT_CSRF_TOKEN_SECRET="dev-csrf-secret-$(date +%s)"
    echo "⚠ Generated development CSRF secret"
fi

# Set defaults
export RRT_ENV="${RRT_ENV:-development}"
export RRT_PORT="${RRT_PORT:-8080}"
export RRT_LOG_LEVEL="${RRT_LOG_LEVEL:-info}"
export RRT_DATABASE_URL="${RRT_DATABASE_URL:-$DB_FILE}"
export RRT_DB_DRIVER="${RRT_DB_DRIVER:-sqlite}"
export RRT_EMBEDDED_DEPS="${RRT_EMBEDDED_DEPS:-true}"
export RRT_THIRD_PARTY_DIR="${RRT_THIRD_PARTY_DIR:-./third_party}"
export RRT_MODELS_DIR="${RRT_MODELS_DIR:-./models}"
export RRT_NETEASE_API_ENDPOINT="${RRT_NETEASE_API_ENDPOINT:-http://127.0.0.1:3300}"

echo ""
echo "Starting RidgeRiceTalk server..."
echo "  Environment: $RRT_ENV"
echo "  Port: $RRT_PORT"
echo "  Database: $RRT_DB_DRIVER ($RRT_DATABASE_URL)"
echo "  Embedded deps: $RRT_EMBEDDED_DEPS"
echo ""

# Run the server
go run ./cmd/server
