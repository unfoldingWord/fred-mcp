#!/bin/sh
set -e

# Start toolbox bound to localhost only — Caddy is the public face.
/usr/local/bin/toolbox \
    --config /app/tools.yaml \
    --address 127.0.0.1 \
    --port 5000 \
    &
TOOLBOX_PID=$!

# Forward signals so Fly can stop both processes cleanly.
trap "kill $TOOLBOX_PID 2>/dev/null; exit 0" TERM INT

# Caddy in the foreground keeps the container alive.
exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
