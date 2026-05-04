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

v1 ships with a single shared bearer token (`FRED_MCP_TOKEN`) enforced
by Caddy in front of the toolbox process. Every MCP request must carry
`Authorization: Bearer <token>`. Token rotation = update the Fly secret
+ update each consumer's MCP config.

Per-user OIDC (Google Workspace) is the planned successor — see
[issue #2](https://github.com/unfoldingWord/fred-mcp/issues/2).
