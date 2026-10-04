#!/usr/bin/env bash
# install-service.sh — render and install the RidgeRiceTalk systemd service.
#
# Usage:
#   sudo ./scripts/install-service.sh \
#        --exec-start /opt/ridgericetalk/server \
#        --working-dir /opt/ridgericetalk \
#        --user ridgericetalk \
#        --env-file /opt/ridgericetalk/.env.production
#
# Defaults assume installation under /opt/ridgericetalk with a dedicated
# `ridgericetalk` system user. Adjust with flags as needed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/ridgericetalk.service.tmpl"

EXEC_START="/opt/ridgericetalk/server"
WORKING_DIR="/opt/ridgericetalk"
SERVICE_USER="ridgericetalk"
ENV_FILE="/opt/ridgericetalk/.env.production"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --exec-start)   EXEC_START="$2"; shift 2 ;;
        --working-dir)  WORKING_DIR="$2"; shift 2 ;;
        --user)         SERVICE_USER="$2"; shift 2 ;;
        --env-file)     ENV_FILE="$2"; shift 2 ;;
        --help|-h)
            sed -n '2,12p' "$0"
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 2
            ;;
    esac
done

if [[ ! -f "$TEMPLATE" ]]; then
    echo "Template not found: $TEMPLATE" >&2
    exit 1
fi

if [[ ! -x "$EXEC_START" ]]; then
    echo "Warning: ExecStart binary not executable or missing: $EXEC_START" >&2
fi

if [[ ! -f "$ENV_FILE" ]]; then
    echo "Warning: env file not found: $ENV_FILE" >&2
fi

# Ensure the service user exists
if ! id "$SERVICE_USER" &>/dev/null; then
    echo "Creating system user: $SERVICE_USER"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# Ensure runtime directories exist and are owned by the service user
for subdir in storage logs; do
    dir="$WORKING_DIR/$subdir"
    mkdir -p "$dir"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$dir"
done

# Render the template
RENDERED="$(mktemp)"
trap 'rm -f "$RENDERED"' EXIT

sed \
    -e "s|{{EXEC_START}}|$EXEC_START|g" \
    -e "s|{{WORKING_DIR}}|$WORKING_DIR|g" \
    -e "s|{{USER}}|$SERVICE_USER|g" \
    -e "s|{{ENV_FILE}}|$ENV_FILE|g" \
    "$TEMPLATE" > "$RENDERED"

# Validate syntax
if command -v systemd-analyze >/dev/null 2>&1; then
    systemd-analyze verify "$RENDERED" || {
        echo "systemd-analyze verify failed" >&2
        exit 1
    }
fi

# Install
INSTALL_PATH="/etc/systemd/system/ridgericetalk.service"
cp "$RENDERED" "$INSTALL_PATH"
chmod 644 "$INSTALL_PATH"

systemctl daemon-reload
systemctl enable ridgericetalk.service

echo "Installed: $INSTALL_PATH"
echo "Enable and start with:  sudo systemctl enable --now ridgericetalk"
echo "View logs with:         journalctl -u ridgericetalk -f"
