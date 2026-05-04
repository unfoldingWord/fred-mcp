# Runbook — onboard a new service consumer

Use this when a worker (e.g. `fred-zulip-bot-worker`) needs to call
fred-mcp.

This is the operational complement to the auth model in
[ADR 0002](../decisions/0002-auth-v2-split-by-consumer-type.md):
each service consumer gets its own named bearer secret, distinct
from the legacy shared `FRED_MCP_TOKEN`.

## Inputs

- A short, stable identifier for the service. Conventionally the
  consumer's repo name, uppercased with hyphens → underscores. For
  `fred-zulip-bot-worker` it's `FRED_ZULIP_BOT_WORKER`.
- Knowledge of which person/repo owns the consumer (you'll hand them
  the token).

## Steps

### 1. Generate and set the Fly secret

```sh
SERVICE=FRED_ZULIP_BOT_WORKER
NEW_TOKEN=$(openssl rand -base64 32)
echo "Token to share with the consumer:"
echo "$NEW_TOKEN"

fly secrets set "FRED_MCP_TOKEN_${SERVICE}=${NEW_TOKEN}" -a fred-mcp
```

`fly secrets set` triggers an automatic redeploy.

### 2. Add a Caddy matcher block

In [`Caddyfile`](../../Caddyfile), add a new matcher + handle block
under the "Service consumers" section:

```caddyfile
@svc_fred_zulip_bot_worker header Authorization "Bearer {env.FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER}"
handle @svc_fred_zulip_bot_worker {
    reverse_proxy 127.0.0.1:5000 {
        header_up X-Service fred-zulip-bot-worker
    }
}
```

Match the matcher name to the service ID with a `svc_` prefix; lowercase
hyphenated for `header_up X-Service`.

### 3. Add the env var to entrypoint.sh's REQUIRED_VARS

In [`entrypoint.sh`](../../entrypoint.sh), add the new var name to
`REQUIRED_VARS`. This is the fail-fast guard — if the secret is
missing, the container refuses to start rather than letting Caddy
substitute empty string and silently match `Authorization: Bearer `
requests.

### 4. PR + merge

The Caddyfile + entrypoint changes from steps 2–3 go in a small PR.
Once merged and deployed, the new matcher is live.

### 5. Hand the token to the consumer maintainer

Send `$NEW_TOKEN` (from step 1) over a secure channel — 1Password
share, Signal, encrypted email. **Not** Slack DM in plaintext.
**Not** committed to git. **Not** in any chat that's archived
broadly.

The consumer side stores it in their own secret store
(e.g. `wrangler secret put FRED_MCP_TOKEN` for a Cloudflare Worker —
the consumer-side secret name is whatever they want; only our Fly
secret name has to match the Caddyfile matcher).

### 6. Smoke test

```sh
curl -i -H "Authorization: Bearer $NEW_TOKEN" \
    https://fred-mcp.fly.dev/mcp \
    -X POST -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# expected: HTTP/2 200 with a JSON-RPC tools list.
```

Confirm in `fly logs -a fred-mcp` that the request shows
`X-Service: <service-id>`.

## Rotating an existing service token

```sh
SERVICE=FRED_ZULIP_BOT_WORKER
NEW_TOKEN=$(openssl rand -base64 32)

# Set the new value first; fly redeploys.
fly secrets set "FRED_MCP_TOKEN_${SERVICE}=${NEW_TOKEN}" -a fred-mcp

# Hand the new token to the consumer; they update their secret.
# Once they've redeployed, the old token is no longer in use anywhere.
```

There's no "old + new accepted simultaneously" window — the secret
is a single value. Coordinate the rotation with the consumer
maintainer to minimize the gap, or accept a brief 401 window during
their redeploy.

## Removing a service consumer

```sh
fly secrets unset "FRED_MCP_TOKEN_${SERVICE}" -a fred-mcp
```

Then in code: remove the matcher block from `Caddyfile`, remove the
var from `entrypoint.sh`'s `REQUIRED_VARS`, ship a PR.
