# fred-mcp

MCP server exposing the unfoldingWord Fred database to MCP clients
(Claude, ChatGPT, Cursor, custom agents).

The server is [`mcp-toolbox`](https://github.com/googleapis/genai-toolbox)
configured by [`tools.yaml`](./tools.yaml). We don't write application
code; we maintain YAML.

See [`docs/architecture.md`](./docs/architecture.md) for the design and
[`docs/decisions/0001-toolbox-as-mcp-server.md`](./docs/decisions/0001-toolbox-as-mcp-server.md)
for the why.

## Connecting from Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

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

(URL and token TBD until first deploy lands.) If your Claude version
doesn't support the `headers` field on remote MCP servers, use
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote) as a stdio
proxy that injects the header.

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
     FRED_MCP_TOKEN="$(openssl rand -base64 48 | tr '+/' '-_' | tr -d '=')"
   ```

   Save `FRED_MCP_TOKEN` to 1Password — you'll hand it out per consumer.

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

Caddy is the trust boundary. Two kinds of bearer are accepted (see
[ADR 0002](./docs/decisions/0002-auth-v2-split-by-consumer-type.md)):

- **Legacy shared `FRED_MCP_TOKEN`** — used by current human consumers
  (Claude Desktop / Code / Cursor). Rotated by updating one Fly secret
  and every consumer's MCP config. To be retired when OIDC for humans
  lands (Auth v2.5, separate issue).
- **Per-service named bearers `FRED_MCP_TOKEN_<SERVICE>`** — one Fly
  secret per service consumer (e.g. `FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER`).
  Rotated independently per service. Logged with an `X-Service` header
  for request attribution.

Adding a new service consumer is a small repeatable change — see
[`docs/runbooks/onboard-service-consumer.md`](./docs/runbooks/onboard-service-consumer.md).

Why two flavors instead of OIDC for humans now: toolbox v1.1.0's
generic auth provider can't validate Google's opaque access tokens
(no RFC 7662 support on Google's side, no JWKS-validatable token in
the OAuth flow). Real OIDC needs a tokeninfo sidecar or a
self-hosted authorization server — tracked separately.
