# ADR 0003: Auth v2.5 — OIDC for humans via tokeninfo sidecar

- **Status:** Accepted
- **Date:** 2026-05-04
- **Depends on:** ADR 0002 (service-token split)
- **Implements:** Issue #7

## Context

ADR 0002 deferred OIDC for human consumers because Google issues opaque access tokens (`ya29.*`) that toolbox v1.1.0 cannot natively validate — no JWT, no RFC 7662 introspection endpoint. Humans continued using the legacy shared `FRED_MCP_TOKEN` with all its v1 problems (laptop sprawl, no per-user identity, manual rotation across consumers).

We need a verification layer that:
1. Works with Google's opaque access tokens.
2. Doesn't require toolbox upstream changes.
3. Doesn't require new infrastructure (self-hosted IdP, token exchange service).
4. Provides per-user identity in logs.
5. Complies with the MCP authorization spec (Protected Resource Metadata, `WWW-Authenticate` header).

## Decision

Add a **Go sidecar** (`tokeninfo-sidecar`) that validates Google access tokens via the `https://oauth2.googleapis.com/tokeninfo` endpoint. Caddy's built-in `forward_auth` directive routes unauthenticated requests to the sidecar before proxying to toolbox.

### Architecture

```
                          :8080
                            │
                          Caddy
          ┌─────────────────┼─────────────────┐
          │                 │                 │
    /healthz            @svc_*           default
    respond 200      X-Service stamp   forward_auth
                      → toolbox            │
                                           ▼
                                     sidecar :5500
                                     /verify
                                      │
                                      ├─ extract Authorization: Bearer
                                      ├─ POST tokeninfo endpoint
                                      ├─ validate aud, hd, email_verified, exp
                                      └─ 200 + X-Auth-Email / X-Auth-Sub
                                          OR 401 + WWW-Authenticate
                                           │ on success
                                           ▼
                                     reverse_proxy
                                     → toolbox :5000
```

### Sidecar validation rules

1. `aud` must match `OAUTH_CLIENT_ID` (our GCP OAuth client).
2. `hd` must match `ALLOWED_HD` (default: `unfoldingword.org`).
3. `email_verified` must be `"true"`.
4. `expires_in` must be > 0.

Results are cached in-memory with TTL = min(expires_in, 60s) to avoid Google tokeninfo rate limits.

### MCP spec compliance

The sidecar serves `GET /.well-known/oauth-protected-resource` (open, no auth) returning the Protected Resource Metadata document per the MCP authorization spec. Caddy routes this path directly to the sidecar without `forward_auth`.

## Options considered

| Option | Verdict | Why |
|--------|---------|-----|
| A. Toolbox-native OIDC | Rejected | Google tokens are opaque; toolbox can't validate them |
| B. Full self-hosted AS (Hydra/Authelia) | Rejected | Overkill — extra infra to operate for a single consumer type |
| **C. Tokeninfo sidecar + Caddy forward_auth** | **Chosen** | ~100 lines Go, no external deps, no new infra, Caddy directive is built-in |
| D. Verify `id_token` JWT server-side | Deferred | Viable fallback if tokeninfo rate limits bite; more complex (JWKS rotation) |

## Consequences

- **First Go code in the repo.** The project's prior philosophy was "ship YAML, not code" (ADR 0001). The sidecar is an intentional, bounded exception — it's infrastructure plumbing, not application logic.
- **Extra process to monitor.** If the sidecar dies, Caddy 502s all OIDC requests. The existing `wait -n` pattern restarts the container. Acceptable at current scale.
- **Google tokeninfo dependency.** Rate limits exist but are undocumented. The 60s cache makes per-user request volume negligible. If hit, fall back to `id_token` JWT verification (Option D).
- **Transition period.** Both legacy bearer and OIDC coexist until all humans migrate. The legacy `@authorized` matcher is removed in a follow-up PR.

## When to revisit

- If Google deprecates the tokeninfo endpoint (unlikely; 10+ year track record).
- If tokeninfo rate limits trigger 401s in production.
- If the MCP authorization spec changes the PRM document shape or discovery flow.
- If service consumers need OAuth (`client_credentials` grant) — that would justify a full AS.
