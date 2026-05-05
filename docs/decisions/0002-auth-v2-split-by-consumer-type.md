# ADR 0002 — Auth: split by consumer type (interim service-token model; OIDC deferred)

- **Status:** Accepted
- **Date:** 2026-05-04
- **Deciders:** Ian Lindsley
- **Supersedes:** none (incremental over the v1 model from #1)
- **Issue:** #2 (this PR), follow-up Auth v2.5 issue (OIDC)

## Context

v1 ships fred-mcp with a single shared bearer (`FRED_MCP_TOKEN`)
enforced by Caddy in front of toolbox. ADR 0001's "When we'd revisit"
section flagged that auth would need to evolve before we onboarded
service consumers.

That moment has arrived: `fred-zulip-bot-worker` is being built in
parallel and will need to call fred-mcp. If the worker bakes the v1
shared-bearer pattern, we'd have to re-cut its auth later. So it
needs its own credential — distinct from whatever humans use, with
its own rotation lifecycle and request-attribution in logs.

We initially tried to bundle this with **OIDC for humans** (Google
Workspace as IdP, toolbox `kind: generic` validating the JWT). PR
review surfaced a hard architectural block:

- Toolbox v1.1.0's `kind: generic` validates JWTs via JWKS, falling
  back to RFC 7662 introspection.
- **Google's OAuth access tokens are opaque** (`ya29.*`), not JWTs.
- **Google does not implement RFC 7662 introspection** — its
  discovery doc has no `introspection_endpoint`, and `tokeninfo` has
  a different response shape.
- The `id_token` is JWT-shaped but is *not* what MCP clients send to
  resource servers per the OAuth 2.1 / MCP authorization spec.

There is no working v1.1.0 toolbox config for "Google as the AS". So
human OIDC is blocked on either (a) toolbox upstream adding
tokeninfo support, or (b) us fronting toolbox with an authorization
server / verification layer of our own. Both are real work.

We decided to ship the worker-needed change now and track OIDC as a
follow-up.

## Decision

**Split the auth model by consumer type. Add per-service named
bearer secrets alongside the legacy shared bearer.**

- **Service consumers** (fred-zulip-bot-worker, future workers) →
  per-service named bearer tokens (`FRED_MCP_TOKEN_<SERVICE>`), one
  Fly secret per consumer.
- **Human consumers** (Claude Desktop, Claude Code, Cursor) → keep
  using the legacy shared `FRED_MCP_TOKEN` for now. **OIDC for
  humans is deferred** to a follow-up issue (Auth v2.5).
- `/healthz` stays open.

Caddy is the trust boundary. It gates on Authorization-header
matchers — one per registered service token, plus the legacy shared
token, plus a default 401. Toolbox runs unchanged (no
`authRequired`).

## Options considered

### Option 1 — Bundle OIDC for humans now (rejected)

Add `kind: authService / type: generic` to `tools.yaml` pointing at
`https://accounts.google.com`; toolbox validates JWTs.

- ❌ **Architecturally broken** (see Context). Google issues opaque
   access tokens; toolbox v1.1.0 can't validate them.
- This was the original v2 design; abandoned after PR review.

### Option 2 — Tokeninfo sidecar for OIDC (deferred to follow-up)

Add a small sidecar that introspects Google access tokens via
`https://oauth2.googleapis.com/tokeninfo`. Caddy `forward_auth`s to
the sidecar; sidecar returns 200 + claim headers if valid, else 401.
Toolbox runs without `authRequired`.

- ✅ Real OIDC for humans, modest extra code (~50 lines Go/Python).
- ✅ Doesn't depend on toolbox upstream changes.
- ❌ Real work to write/test/operate. Not on the critical path for
   the worker.
- **Tracked as Auth v2.5 in a follow-up issue.**

### Option 3 — Self-hosted authorization server (rejected for now)

Run Hydra/Authelia/Dex/custom that issues JWT access tokens and
federates to Google.

- ❌ Premature for current consumer set.
- Reserved for the case where we outgrow per-service bearers and
  Option 2 stops scaling.

### Option 4 — Service-token split, defer OIDC (chosen)

What's described above. Adds the worker's named bearer alongside the
legacy shared bearer. No infrastructure change.

- ✅ Unblocks the worker immediately.
- ✅ Trivial implementation: a few Caddyfile matchers + an entrypoint
   env-var guard.
- ✅ Doesn't preclude any of the other options for human auth.
- ❌ Humans still hold a static shared bearer (the v1 problem) until
   Auth v2.5 lands.

## Why Option 4 is "split by consumer type" if humans haven't moved

Even though humans keep the legacy shared bearer, the *split* is
meaningful:

- The worker has its own credential. Worker compromise doesn't force
  rotating the human token (and vice versa).
- Per-service Fly secrets give per-consumer rotation and
  request-attribution in logs (`X-Service` header stamped by the
  matcher block).
- Adding any future service consumer is the same lightweight
  pattern, not a redesign.

The "human OIDC" part of the split lands in v2.5; the structure is
already in place to receive it.

## Consequences

- **Operational:** still one Fly app, one container, one toolbox
  process. Caddyfile gains a matcher block per service consumer.
- **Security model (humans):** unchanged from v1 — laptop sprawl,
  manual rotation, no per-user identity. Documented as known debt
  until v2.5.
- **Security model (services):** narrower threat profile than v1.
  Worker token at rest lives in Cloudflare Workers' secret store,
  not on dev laptops. Independent rotation. Per-request attribution.
- **Adding a new service consumer:** new Fly secret + new Caddy
  matcher + new line in `entrypoint.sh`'s `REQUIRED_VARS`. Bounded
  per-onboarding cost.
- **Empty-env-var trap:** Caddy substitutes empty string for missing
  env vars, which would make the matcher silently accept literal
  `Authorization: Bearer ` requests. The entrypoint fails fast at
  startup if any expected token var is unset or empty, so this state
  shouldn't reach Caddy in practice.

## When we'd revisit

- Auth v2.5 ships and humans move to OIDC → delete the legacy
  `FRED_MCP_TOKEN` Fly secret + the `@authorized` matcher block.
- Service consumers grow past ~3-4 → revisit whether per-service
  matchers in Caddy still scale, or move services to OAuth
  client_credentials against the v2.5 AS.
