# Runbook: Cutover from Legacy Bearer to OIDC

Transition human consumers from the shared `FRED_MCP_TOKEN` to Google OIDC authentication.

## Prerequisites

- Step 0 (GCP OAuth setup) complete — `OAUTH_CLIENT_ID` and `TOOLBOX_URL` Fly secrets set.
- Sidecar deployed and passing smoke tests (PRM endpoint returns valid JSON, unauthenticated requests return 401).

## Phase 1: Deploy with both auth paths active

The updated Caddyfile supports both the legacy `@authorized` matcher and the new `forward_auth` handler simultaneously. No consumer disruption.

```bash
fly deploy -a fred-mcp
```

Verify:
```bash
# Legacy token still works:
curl -s -H "Authorization: Bearer $FRED_MCP_TOKEN" https://fred-mcp.fly.dev/mcp

# Service tokens still work:
curl -s -H "Authorization: Bearer $FRED_MCP_TOKEN_FRED_ZULIP_BOT_WORKER" https://fred-mcp.fly.dev/mcp

# PRM document accessible:
curl -s https://fred-mcp.fly.dev/.well-known/oauth-protected-resource | jq .
```

## Phase 2: Migrate human consumers

### Claude Code

```bash
claude mcp remove fred  # if previously configured with bearer
claude mcp add --transport http fred https://fred-mcp.fly.dev/mcp
```

On first tool call, Claude Code will:
1. Fetch `/.well-known/oauth-protected-resource`
2. Discover Google as the authorization server
3. Trigger the Google OAuth loopback flow
4. Cache the access token

### Claude Desktop

Update `claude_desktop_config.json`:
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

### Verify each consumer

Check Fly logs for `X-Auth-Email` headers on incoming requests:
```bash
fly logs -a fred-mcp | grep X-Auth-Email
```

## Phase 3: Remove legacy bearer

Once all human consumers are confirmed using OIDC (check logs for any remaining `FRED_MCP_TOKEN` usage):

1. Remove `@authorized` block from `Caddyfile`.
2. Remove `FRED_MCP_TOKEN` from `entrypoint.sh` REQUIRED_VARS.
3. PR + merge + deploy.
4. Delete the Fly secret:
   ```bash
   fly secrets unset FRED_MCP_TOKEN -a fred-mcp
   ```

## Rollback

If OIDC is broken in production:
1. The legacy `@authorized` matcher is still active during transition — humans can fall back to their old token.
2. If already past Phase 3: re-set `FRED_MCP_TOKEN` as a Fly secret and re-add the `@authorized` matcher.
