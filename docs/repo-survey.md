# Repo survey — what we learned before designing fred-mcp

This file records what we found when surveying the three repos that
informed the `fred-mcp` design. Captured 2026-04-29 to 2026-05-03.

## 1. fred-zulip-bot

**What it is:** Python 3.13 / FastAPI Zulip bot that lets users ask
natural-language questions about the Fred database. Generates SQL via an
LLM, runs it, returns results.

**Database:** MariaDB (called "Fred"). The schema is in
`fred-zulip-bot/context/DDLs.rtf` — 27 tables in the
`uw-data-tracking-stage` schema. Key tables for our purposes:

| Table | Notes |
|---|---|
| `language_engagements` | Core: a (language, country, partner org) row. PK `language_engagement_id`. |
| `organizations` | Translation partners. PK `org_id`, unique `name`. |
| `uw_translation_products` | Per-engagement scripture products (status, dates). FK to `language_engagements`. |
| `language_engagement_metadata` | Free-text notes per engagement. |
| `uw_product_metadata` | Free-text notes per product. |
| `master_uw_language_engagements` | Denormalized read-model joining engagements + languages + countries + orgs + status. |
| `master_uw_translation_projects` | Denormalized read-model for products. |
| `pb_language_data` | Progress Bible-sourced per-language status (chapters, AAG, etc.). |
| `joshua_project_data` | Joshua Project people-group data. |
| `training_events` | UW training events (FK to country, org, program). |
| `countries`, `ietf_languages_codes` | Reference data. |
| `programs`, `portfolios` | Program structure. |

Several tables use MariaDB system versioning (`WITH SYSTEM VERSIONING`),
which enables `FOR SYSTEM_TIME` queries — relevant for any change-over-time
questions.

**DB access pattern:**
`fred-zulip-bot/fred_zulip_bot/adapters/mysql_client.py` — `MySqlClient`
opens a fresh connection per query, executes, closes. No pooling. Returns
rows as comma-joined strings.

**SQL safety:** `fred-zulip-bot/fred_zulip_bot/services/sql_service.py`
uses regex guards to allow only `SELECT` and block `DROP`, `ALTER`,
`INSERT`, comment markers, file ops. Good enough for an LLM-on-LLM
guardrail; toolbox replaces it with parameterized queries + read-only
DB role.

**Domain knowledge embedded in the system prompt:**
`fred-zulip-bot/context/system_prompt_rules.txt` — this file is a goldmine.
It tells the LLM things like:

- "A language is Strategic iff `resource_level >= 1`"
- "Use `pb_language_data` for all-languages-in-a-country, use
  `language_engagements` for languages-uW-is-working-on"
- "Always join on `iso_639_2`, never on `subtag_new`"
- "UPG = `jpscale <= 1`"
- "When searching language name use `LIKE`; format is `language [code]`"

We ship these rules as an MCP **prompt** so any client (Claude, ChatGPT,
etc.) can pull them in — not just the Zulip bot. This is one of the
biggest wins of the toolbox-as-MCP-server approach.

## 2. fia-mcp

**What it is:** TypeScript MCP server using `@modelcontextprotocol/sdk`,
deployed to Cloudflare Workers, fronting the FIA GraphQL API.

**Layout we'd have copied if we'd written our own server:**
- `src/index.ts` — `McpServer` instance, tools registered via
   `.registerTool(name, { description, inputSchema: zodSchema }, handler)`
- `src/tools/*` — one file per tool
- `src/auth.ts` — bearer token, timing-safe compare, `MCP_SHARED_SECRET`
- `src/graphql.ts` — wrapped client (would have been `mysql.ts` for us)
- Unified `wrapHandler()` that returns `{content: [{type: 'text', text}], _meta: {...}}`

**Fitness functions we admire (and may steal patterns from for fred-mcp):**
- ESLint + Prettier
- `tsc --noEmit` type check
- Vitest with the Cloudflare Workers pool
- `dependency-cruiser` forbidding circular deps and cross-imports between
   tool handlers (architectural fitness)
- `npm audit --omit=dev` in CI

**CI/CD:** GitHub Actions with sequential jobs:
`security-audit → lint → typecheck → architecture → test → build`,
then env-specific deploy workflows.

**Why we're not copying fia-mcp wholesale:** see ADR 0001. Almost all of
its complexity is solving problems toolbox solves natively (MCP protocol,
tool registration, serialization). We don't need a TS layer to wrap SQL.

What we *might* port to a `fred-mcp` repo even with toolbox:
- A GitHub Actions workflow that lints `tools.yaml`, validates SQL
   syntactically (e.g., `mariadb --execute "EXPLAIN ..."` against a test
   DB), and deploys to Fly on merge to `main`.
- A `staging` Fly app + a `production` Fly app, with promotion gating.

## 3. mcp-toolbox

**What it is:** Google open-source Go binary (`github.com/googleapis/mcp-toolbox`).
Reads `tools.yaml`, opens DB pools, serves MCP over HTTP.

**Concepts:**
- `kind: source` — DB connection (MySQL/MariaDB, Postgres, Spanner,
   BigQuery, Snowflake, Looker, ~30 supported).
- `kind: tool` — an LLM-callable operation. Either a built-in type
   (e.g., `mysql-list-tables`) or a custom SQL one (`mysql-sql`).
- `kind: toolset` — named bundle of tools to expose to a particular
   client.
- `kind: prompt` — MCP prompt resources. `type: custom` lets us ship a
   `messages` list with `{role, content}` entries.

**Built-in MariaDB/MySQL tools we get for free:**
`execute_sql`, `list_active_queries`, `get_query_plan`, `list_tables`,
`list_table_stats`, `list_tables_missing_unique_indexes`, plus more.
Source: `mcp-toolbox/internal/prebuiltconfigs/tools/mysql.yaml`.

**Custom SQL tool example:**
```yaml
kind: tool
name: get_language_engagement
type: mysql-sql
source: fred
description: |
  Look up a language engagement by IETF code and country (alpha-3).
  ...
statement: |
  SELECT le.*, o.name AS org_name
  FROM language_engagements le
  JOIN organizations o ON o.org_id = le.translation_organization_id
  WHERE le.ietf_id = ? AND le.country_id = ?
parameters:
  - name: ietf_id
    type: integer
    description: IETF language ID (from ietf_languages_codes.ietf_id)
  - name: country_id
    type: string
    description: ISO alpha-3 country code (e.g., "USA", "IND")
```

**Auth:** OIDC. `internal/auth/google` (Google ID tokens) and
`internal/auth/generic` (any JWT issuer). Tools can require an auth
service and bind verified claims to query parameters
(`WHERE user_id = $auth.sub`). Powerful but heavier than `fia-mcp`'s
shared bearer token; we defer to v2.

**Deployment:** official container at
`us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:$VERSION`.
Works fine on Fly.io. Does **not** work on Cloudflare Workers (Go binary,
needs a long-running process). Removing the Workers constraint is the
reason Fly.io became attractive.

**Limits to know:**
- No NL2SQL — the LLM picks tools, doesn't get help generating SQL.
- No response shaping in v1 — SQL output is what the LLM sees.
- No auto-generation of tools from schema — every tool is hand-written.
- YAML expressivity is limited; logic must live in SQL or be added as a
   separate service.

## How this maps to fred-mcp

| fred-zulip-bot has | fred-mcp will |
|---|---|
| Hand-rolled `mysql_client.py` with no pooling | Inherit toolbox's pool |
| Tuple-as-string responses | JSON rows from toolbox |
| Regex SQL guard | Read-only DB role + parameterized queries |
| Per-query connection | Pooled, reused |
| Locked to Zulip | Reusable from any MCP client |
| System prompt rules baked into the bot | Shipped as an MCP prompt resource |
| LLM generates SQL freely | Curated tools first; `execute_sql` as escape hatch |
