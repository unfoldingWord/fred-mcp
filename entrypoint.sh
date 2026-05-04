#!/usr/bin/env bash
set -euo pipefail

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

# Forward signals so Fly can stop both processes cleanly.
trap 'kill $TOOLBOX_PID $CADDY_PID 2>/dev/null; exit 0' TERM INT

# Exit when ANY child dies, so Fly restarts the container instead of
# leaving Caddy serving /healthz while toolbox is dead.
wait -n
EXIT=$?
echo "fred-mcp: a child process exited with status $EXIT; shutting down container" >&2
kill -TERM $TOOLBOX_PID $CADDY_PID 2>/dev/null || true
exit $EXIT
