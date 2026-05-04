# ADR 0001 — Use mcp-toolbox as the MCP server, not as a library

- **Status:** Accepted
- **Date:** 2026-05-03
- **Deciders:** Ian Lindsley

## Context

unfoldingWord needs an MCP server that exposes the "Fred" MariaDB
database to MCP clients (Claude, ChatGPT, custom agents). The
`fred-zulip-bot` already proves the value of LLM access to this data,
but its DB integration (`fred_zulip_bot/adapters/mysql_client.py`) is
hand-rolled, returns rows as comma-joined strings, has no connection
pooling, and is locked to a single client (Zulip).

Two reference points shaped the decision:

1. **`fia-mcp`** — a TypeScript MCP server using `@modelcontextprotocol/sdk`
   on Cloudflare Workers. Solid scaffolding (Wrangler config, dependency
   cruiser, Vitest, GitHub Actions). Hand-written tool handlers, hand-rolled
   GraphQL client, custom bearer-token auth.
2. **`mcp-toolbox`** — Google open-source Go binary. Reads a `tools.yaml`,
   opens a connection pool to a database, and serves an MCP endpoint
   directly. Ships with ~25 prebuilt MySQL tools and a custom-tool DSL.

## Options considered

### Option A — toolbox is the MCP server (chosen)

Run the official `mcp-toolbox` container on Fly.io. Define everything in
`tools.yaml`. We ship YAML, not code.

- ✅ Zero application code to maintain.
- ✅ Connection pooling, retries, observability, MCP protocol compliance
   are someone else's problem.
- ✅ ~25 MariaDB introspection tools out of the box (`list_tables`,
   `get_query_plan`, `execute_sql`, etc.) — useful as both the DBA-style
   surface and the escape hatch.
- ✅ Adding a new tool = ~8 lines of YAML.
- ✅ Same MCP wire protocol as a hand-written server — clients can't tell.
- ❌ Can't add non-SQL tools (e.g., calls to a REST API) to the same server.
- ❌ Can't reshape responses; rows come back as JSON exactly as SQL returns.
- ❌ Auth model is OIDC-flavored; doesn't natively match `fia-mcp`'s
   shared-bearer-token pattern.

### Option B — Write our own TS MCP server (fia-mcp clone) on Fly

Copy `fia-mcp`'s scaffolding wholesale, swap GraphQL for `mysql2`, write
~10 tool handlers in TypeScript.

- ✅ Full control over response shape, auth, mixed tool types.
- ✅ Can reuse `fia-mcp`'s CI, fitness functions, deployment patterns.
- ❌ Re-implements connection pooling, MCP protocol details, observability.
- ❌ Every new tool is a code change with tests, review, deploy cycle.
- ❌ Drifts from being a "just SQL" surface — the simplicity advantage of
   a database-as-MCP server gets lost in handler boilerplate.

### Option C — Hybrid: thin TS facade in front of toolbox

TS MCP server handles auth + custom tools; proxies SQL tools to a toolbox
container running alongside.

- ✅ Best of both — toolbox handles SQL, TS handles weird stuff.
- ❌ Two services to operate. Two deployments. More config surface.
- ❌ Premature: we have no concrete need for "weird stuff" yet.

## Decision

**Option A.** Toolbox runs as the MCP server. We define everything in
`tools.yaml`. We deploy to Fly.io.

Rationale: ~95% of `fred-zulip-bot`'s LLM utility is "run a parameterized
SQL query and return rows." That's exactly toolbox's sweet spot. The
remaining 5% (custom orchestration, auth-bound queries) is hypothetical
right now and not worth designing for.

The `execute_sql` escape hatch + a read-only DB role gives the LLM
flexibility for questions our curated tools don't cover, without us
having to predict every question up front.

## When we'd revisit

We move to **Option C** (not B) if any of:

1. We need a tool that joins SQL data with a non-SQL source (a REST API,
   another MCP server, a vector DB).
2. We need server-side response transformation that pure SQL can't express
   (e.g., calling an LLM mid-tool to summarize results).
3. We need per-user auth where the user's identity must constrain the SQL
   (and toolbox's OIDC + auth-bound parameters don't fit our identity
   provider).
4. `tools.yaml` exceeds ~50 tools and the lack of templating/composition
   becomes painful.

We do **not** move to B in any scenario — once we have toolbox handling
SQL well, throwing it out to write our own SQL handlers is pure
regression.

## Consequences

- **Operational:** one Fly app, one container, one config file. No
   build pipeline beyond a Dockerfile that pins a toolbox version.
- **Security:** the read-only DB user is the primary safety boundary, not
   the application code. Provisioning that user correctly is critical.
   See bootstrap issue.
- **Auth:** v1 ships with either no auth or a Fly-level bearer token in
   front. Production-grade OIDC is deferred.
- **Lock-in:** moderate. `tools.yaml` is portable in concept but tied to
   toolbox's schema. If toolbox is ever abandoned, we'd port the SQL
   statements into whatever replaces it — not a catastrophic migration.
- **Observability:** toolbox supports OpenTelemetry. We get traces and
   metrics without instrumenting anything ourselves.
