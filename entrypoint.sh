#!/usr/bin/env bash
set -euo pipefail

# Required env vars. The container fails-fast if any are unset/empty
# rather than letting Caddy proxy with an empty-string matcher (which
# would silently match `Authorization: Bearer ` requests).
#
# Keep this list in sync with the @matcher blocks in Caddyfile —
# every FRED_MCP_TOKEN_* env var referenced there must be listed here.
REQUIRED_VARS=(
    FRED_MCP_TOKEN
    FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER
    OAUTH_CLIENT_IDS
    TOOLBOX_URL
    MYSQL_HOST
    MYSQL_DATABASE
    MYSQL_USER
    MYSQL_PASSWORD
)

missing=()
for v in "${REQUIRED_VARS[@]}"; do
    if [[ -z "${!v:-}" ]]; then
        missing+=("$v")
    fi
done
if (( ${#missing[@]} > 0 )); then
    echo "fred-mcp: missing required env vars: ${missing[*]}" >&2
    echo "fred-mcp: refusing to start (see ADR 0002 for context)" >&2
    exit 1
fi

# Tokeninfo sidecar — validates Google OAuth tokens for human consumers.
# Bound to localhost; Caddy forward_auth's to it on :5500.
/usr/local/bin/tokeninfo-sidecar &
SIDECAR_PID=$!

# Toolbox bound to localhost only — Caddy is the public face.
# log-level DEBUG = highest verbosity supported by toolbox (no TRACE).
/usr/local/bin/toolbox \
    --config /app/tools.yaml \
    --address 127.0.0.1 \
    --port 5000 \
    --log-level DEBUG \
    &
TOOLBOX_PID=$!

caddy run --config /etc/caddy/Caddyfile --adapter caddyfile &
CADDY_PID=$!

# Forward signals so Fly can stop all processes cleanly.
trap 'kill $SIDECAR_PID $TOOLBOX_PID $CADDY_PID 2>/dev/null; exit 0' TERM INT

# Exit when ANY child dies, so Fly restarts the container instead of
# leaving Caddy serving /healthz while toolbox is dead.
wait -n
EXIT=$?
echo "fred-mcp: a child process exited with status $EXIT; shutting down container" >&2
kill -TERM $SIDECAR_PID $TOOLBOX_PID $CADDY_PID 2>/dev/null || true
exit $EXIT
