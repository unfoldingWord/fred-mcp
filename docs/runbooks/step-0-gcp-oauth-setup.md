# Runbook: Step 0 — GCP OAuth Setup

One-time setup of the Google Cloud OAuth client that human consumers use to authenticate against fred-mcp.

## Prerequisites

- Google Workspace admin (or sufficient IAM) in the unfoldingWord org.
- `gcloud` CLI installed and authenticated (`gcloud auth login`).

## Steps

### 1. Create GCP project

```bash
gcloud projects create fred-mcp-prod --organization=<ORG_ID>
gcloud config set project fred-mcp-prod
```

If the project already exists, just set it as active.

### 2. Configure OAuth consent screen

1. Go to [APIs & Services > OAuth consent screen](https://console.cloud.google.com/apis/credentials/consent).
2. **User type: Internal** (load-bearing — restricts sign-in to unfoldingWord Workspace accounts only).
3. App name: `Fred MCP`
4. Support email: your team email.
5. Scopes: `openid`, `email`, `profile`.
6. Save.

### 3. Create OAuth 2.0 Client ID

1. Go to [APIs & Services > Credentials](https://console.cloud.google.com/apis/credentials).
2. **Create credentials > OAuth client ID**.
3. Application type: **Web application**.
4. Name: `fred-mcp-oauth`
5. Authorized redirect URIs:
   - `https://claude.ai/api/mcp/auth_callback` (Claude AI web)
   - `http://localhost:<port>/callback` (Claude Code loopback — add the specific port when first-use error reveals it)
6. Create.

### 4. Capture credentials

Note the **Client ID** (looks like `123456789-abc.apps.googleusercontent.com`).

The Client Secret is needed by MCP clients that implement the full OAuth 2.1 flow. Store it securely.

### 5. Set Fly secrets

```bash
fly secrets set OAUTH_CLIENT_ID=<client-id> -a fred-mcp
fly secrets set TOOLBOX_URL=https://fred-mcp.fly.dev -a fred-mcp
```

### 6. Verify

After deploying the updated container:

```bash
# PRM document should be accessible:
curl -s https://fred-mcp.fly.dev/.well-known/oauth-protected-resource | jq .

# Unauthenticated request returns 401 with WWW-Authenticate:
curl -s -i https://fred-mcp.fly.dev/mcp | head -5
```

## Notes

- The consent screen being **Internal** is the primary access gate. Only unfoldingWord Workspace accounts can complete sign-in.
- The sidecar's `hd` check is defense-in-depth (catches consent-screen misconfig).
- Cursor: add its redirect URI when Cursor onboards as a consumer.
