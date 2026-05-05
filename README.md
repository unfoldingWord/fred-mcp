# fred-mcp

MCP server exposing the unfoldingWord Fred database to MCP clients
(Claude, ChatGPT, Cursor, custom agents).

The server is [`mcp-toolbox`](https://github.com/googleapis/genai-toolbox)
configured by [`tools.yaml`](./tools.yaml). We don't write application
code; we maintain YAML.

See [`docs/architecture.md`](./docs/architecture.md) for the design and
[`docs/decisions/0001-toolbox-as-mcp-server.md`](./docs/decisions/0001-toolbox-as-mcp-server.md)
for the why.

## Connecting as a human consumer (OIDC)

Human consumers authenticate via Google Workspace OAuth. The server
validates that tokens were issued specifically for fred-mcp (audience
binding), so MCP clients must be configured with the server's OAuth
client credentials. Get the client ID and secret from your team lead
or 1Password.

### Claude Code

```bash
claude mcp add --transport http \
  --header "Authorization: Bearer $(gcloud auth print-access-token \
    --client-id-file=<path-to-client-secret.json>)" \
  fred https://fred-mcp.fly.dev/mcp
```

Or configure in `.claude/settings.json` with explicit OAuth parameters.
Claude Code will handle the Google OAuth loopback flow using the
provided client credentials.

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "fred": {
      "url": "https://fred-mcp.fly.dev/mcp",
      "auth": {
        "type": "oauth",
        "client_id": "<OAUTH_CLIENT_ID>",
        "client_secret": "<OAUTH_CLIENT_SECRET>",
        "authorization_url": "https://accounts.google.com/o/oauth2/v2/auth",
        "token_url": "https://oauth2.googleapis.com/token",
        "scope": "openid email profile"
      }
    }
  }
}
```

Sign in with your `@unfoldingword.org` Google account when prompted.

### Legacy bearer (transition period)

During the transition, the legacy `FRED_MCP_TOKEN` bearer still works:

```json
{
  "mcpServers": {
    "fred": {
      "url": "https://fred-mcp.fly.dev/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_FRED_MCP_TOKEN_HERE"
      }
    }
  }
}
```

This will be removed once all humans migrate to OIDC.

## Local development

Build and run the full image (toolbox + Caddy):

```sh
docker build -t fred-mcp:local .
docker run --rm -p 8080:8080 --env-file .env.local fred-mcp:local
```

Where `.env.local` (do not commit) contains:

```
MYSQL_HOST=...
MYSQL_PORT=3306
MYSQL_DATABASE=uw-data-tracking-stage
MYSQL_USER=fred_mcp_ro
MYSQL_PASSWORD=...
FRED_MCP_TOKEN=local-dev-token
FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER=local-dev-worker-token
OAUTH_CLIENT_IDS=your-client-id.apps.googleusercontent.com
TOOLBOX_URL=http://localhost:8080
```

Smoke test:

```sh
# Health check (no auth):
curl -i http://localhost:8080/healthz
# → 200 ok

# Without token: rejected.
curl -i http://localhost:8080/mcp
# → 401

# With token: proxied to toolbox.
curl -i -H "Authorization: Bearer local-dev-token" \
  http://localhost:8080/mcp
# → toolbox response
```

## Deploying to Fly

Pre-reqs (one-time, not in this repo):

1. Read-only DB user (`fred_mcp_ro`) provisioned on Fred — see
   [`docs/issues/001-toolbox-mcp-bootstrap.md`](./docs/issues/001-toolbox-mcp-bootstrap.md).
2. Fly app created: `fly launch --no-deploy --name fred-mcp`.
3. Fly secrets set:

   ```sh
   fly secrets set \
     MYSQL_HOST=<fred-host> \
     MYSQL_PORT=3306 \
     MYSQL_DATABASE=uw-data-tracking-stage \
     MYSQL_USER=fred_mcp_ro \
     MYSQL_PASSWORD=<from-DBA> \
     FRED_MCP_TOKEN="$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=')" \
     OAUTH_CLIENT_IDS=<from-gcp-console> \
     TOOLBOX_URL=https://fred-mcp.fly.dev
   ```

   Save `FRED_MCP_TOKEN` to 1Password — you'll hand it out per consumer.
   See [`docs/runbooks/step-0-gcp-oauth-setup.md`](./docs/runbooks/step-0-gcp-oauth-setup.md) for `OAUTH_CLIENT_IDS` setup.

Then:

```sh
fly deploy
fly status
```

Verify:

```sh
curl -i https://fred-mcp.fly.dev/healthz                              # 200
curl -i https://fred-mcp.fly.dev/mcp                                  # 401
curl -i -H "Authorization: Bearer $TOKEN" https://fred-mcp.fly.dev/mcp # tool list
```

## Auth model

Caddy is the trust boundary. Three auth paths are supported (see
[ADR 0002](./docs/decisions/0002-auth-v2-split-by-consumer-type.md) and
[ADR 0003](./docs/decisions/0003-auth-v25-oidc-tokeninfo-sidecar.md)):

- **Google OIDC (humans)** — MCP clients discover auth requirements via
  the [Protected Resource Metadata](https://fred-mcp.fly.dev/.well-known/oauth-protected-resource)
  endpoint, then authenticate with Google OAuth. A tokeninfo sidecar
  validates the access token and stamps `X-Auth-Email` / `X-Auth-Sub`
  headers for per-user attribution. Only unfoldingWord Workspace
  accounts are accepted (`hd == unfoldingword.org`).
- **Per-service named bearers `FRED_MCP_TOKEN_<SERVICE>`** — one Fly
  secret per service consumer (e.g. `FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER`).
  Rotated independently per service. Logged with an `X-Service` header
  for request attribution.
- **Legacy shared `FRED_MCP_TOKEN`** (transition period) — still
  accepted for human consumers that haven't migrated to OIDC yet. Will
  be removed once all humans are on OIDC.

Adding a new service consumer is a small repeatable change — see
[`docs/runbooks/onboard-service-consumer.md`](./docs/runbooks/onboard-service-consumer.md).

OIDC setup: see
[`docs/runbooks/step-0-gcp-oauth-setup.md`](./docs/runbooks/step-0-gcp-oauth-setup.md)
for initial GCP configuration and
[`docs/runbooks/cutover-oidc.md`](./docs/runbooks/cutover-oidc.md)
for the migration plan.
