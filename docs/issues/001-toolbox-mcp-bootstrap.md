# Bootstrap fred-mcp: deploy mcp-toolbox to Fly with a starter tools.yaml

> ## ⚠️ Before you start: two things to know
>
> **1. The `tools.yaml` in this issue may not parse cleanly against the
> pinned toolbox version on the first try.** It was written by reading
> the toolbox source on the current `main` branch, but the issue pins
> the `1.1.0` release container. Field names and YAML structure can
> drift between releases. Two specific spots to verify against
> `1.1.0`:
>
> - **Top-level structure.** This issue's YAML uses a single document
>   with top-level `sources:` / `tools:` / `toolsets:` / `prompts:`
>   maps. Every prebuilt config in the toolbox repo (e.g.
>   [`postgres.yaml`][prebuilt-pg]) instead uses the multi-document
>   form: separate `kind: source` / `kind: tool` / `kind: toolset` /
>   `kind: prompt` blocks separated by `---`. If the mapping form
>   doesn't parse, mechanically convert to the multi-document form by
>   copying the prebuilt config's structure.
> - **Custom-SQL parameter placeholders.** The custom queries below use
>   `?` (standard MariaDB positional placeholders). This is almost
>   certainly correct for the `mysql-sql` tool type, but no worked
>   example with parameters is checked in to the toolbox repo to copy
>   from. If queries fail with a placeholder error, check
>   [`mysqlsql_test.go`][mysql-test] for the canonical syntax.
>
> Either issue will surface during the local `docker run` smoke test
> (step 5 below) before any deploy. Budget ~30 minutes for adjustments.
>
> **2. Auth is enforced at the edge by Caddy, not by toolbox.** Toolbox
> listens on `127.0.0.1` inside the container; Caddy is the only
> process that binds the public port and it requires
> `Authorization: Bearer $FRED_MCP_TOKEN` on every request. This is
> intentional — it gives us a working auth story in v1 without
> committing to OIDC, and the bearer token is easy to rotate.
>
> [prebuilt-pg]: https://github.com/googleapis/mcp-toolbox/blob/main/internal/prebuiltconfigs/tools/postgres.yaml
> [mysql-test]: https://github.com/googleapis/mcp-toolbox/blob/main/internal/tools/mysql/mysqlsql/mysqlsql_test.go

## Goal

Stand up the first working version of `fred-mcp`: an MCP server that
exposes the Fred MariaDB database to MCP clients, deployed on Fly.io,
using [Google's mcp-toolbox][toolbox] container.

When this issue is closed, a developer should be able to:

1. Add `https://fred-mcp.fly.dev/mcp` (or whatever the final URL is) to
   Claude Desktop / Cursor / any MCP client.
2. Ask "what language engagements does unfoldingWord have in India?" and
   get a sensible answer the LLM derived by calling tools defined in
   `tools.yaml`.
3. Ask a question outside the curated tool surface and have the LLM use
   the `execute_sql` escape hatch to write a query.
4. Pull in the "Fred query rules" MCP prompt and have the LLM follow the
   domain conventions (Strategic = `resource_level >= 1`, etc.).

## Why this approach

See [`docs/decisions/0001-toolbox-as-mcp-server.md`](../decisions/0001-toolbox-as-mcp-server.md).
Short version: toolbox **is** the MCP server. We ship a `tools.yaml`,
not application code.

See [`docs/architecture.md`](../architecture.md) for the mental model.

## Auth model for v1

A single shared bearer token, enforced by Caddy in front of toolbox.
Concretely:

- A Caddy reverse proxy runs in the same container as toolbox and binds
  the public port (8080).
- Toolbox itself binds only `127.0.0.1:5000` (internal to the
  container) — it cannot be reached from the network.
- Every public request must include
  `Authorization: Bearer $FRED_MCP_TOKEN`. Caddy returns `401` for any
  request without a matching token.
- The token is a Fly secret (`FRED_MCP_TOKEN`), generated as a long
  random string. We hand it out per-consumer (Ian's Claude Desktop,
  the staging Zulip bot replacement, etc.) and rotate by changing the
  secret + updating consumers.

This is intentionally not OIDC. It's the simplest scheme that gives us
a real auth boundary, supports any MCP client (you just put the header
in their config), and is rotatable. Per-user identity, user-bound
query parameters, and OIDC flows are deferred to a future issue.

## Non-goals (explicitly deferred)

- **Per-user identity / OIDC.** v1 has one shared token; we can't tell
  which client is making a given request beyond Fly access logs. Fine
  for an internal tool with a small number of trusted consumers.
- **Write tools.** DB user is read-only.
- **CI/CD.** Manual `fly deploy` for v1. GitHub Actions comes in a
  follow-up.
- **Staging vs production split.** Single Fly app for v1.
- **Schema-derived tool generation.** We hand-write the starter tools.

## Pre-reqs (do these first, in order)

### 1. Provision a read-only MariaDB user for the toolbox

This is the **primary safety boundary**. The `execute_sql` escape hatch
is only safe because of this. Ask the Fred DBA to run the following on
the staging instance first, then the production instance:

```sql
-- Adjust DB name if Fred's actual schema differs.
CREATE USER 'fred_mcp_ro'@'%' IDENTIFIED BY '<strong-random-password>';

-- Read-only on the data schema.
GRANT SELECT ON `uw-data-tracking-stage`.* TO 'fred_mcp_ro'@'%';
GRANT SELECT ON `uw-data-tracking`.* TO 'fred_mcp_ro'@'%';

-- Allow EXPLAIN (used by get_query_plan).
-- SELECT grant covers EXPLAIN on tables; no extra grant needed in MariaDB.

-- Allow PROCESS for list_active_queries (optional; omit if too sensitive).
-- GRANT PROCESS ON *.* TO 'fred_mcp_ro'@'%';

FLUSH PRIVILEGES;
```

Verify:
```sql
SHOW GRANTS FOR 'fred_mcp_ro'@'%';
-- Confirm: only SELECT (and optionally PROCESS). No INSERT/UPDATE/DELETE/DROP.
```

Save the password somewhere we can put into a Fly secret in the next step.

### 2. Create the Fly app

```sh
fly launch --no-deploy --name fred-mcp --org unfoldingword
# Or whatever org we use.
```

Generate a strong shared bearer token:
```sh
# 48 random bytes, base64url-encoded — looks like:
#   K3pv...x2_w  (64 chars)
openssl rand -base64 48 | tr '+/' '-_' | tr -d '='
```

Set the secrets (DB credentials + bearer token):
```sh
fly secrets set \
  MYSQL_HOST=<fred-host> \
  MYSQL_PORT=3306 \
  MYSQL_DATABASE=uw-data-tracking-stage \
  MYSQL_USER=fred_mcp_ro \
  MYSQL_PASSWORD=<the-password-from-step-1> \
  FRED_MCP_TOKEN=<the-token-from-openssl-above>
```

Save the bearer token in 1Password / shared secret store. We'll need it
to configure each MCP client.

### 3. Confirm Fly can reach Fred

The Fred MariaDB instance has to be reachable from Fly's egress IPs.
Two options:

- **A.** Public IP + IP allowlist for Fly's egress range.
- **B.** Private connectivity (Fly WireGuard / VPC peering).

Pick whichever Fred's network already supports. If unsure, A is faster
to ship. Confirm with the DBA before deploying.

## Files to create

```
fred-mcp/
├── tools.yaml          # the MCP server definition (see below)
├── Caddyfile           # bearer-token enforcement, proxy to toolbox
├── entrypoint.sh       # starts toolbox + Caddy in one container
├── Dockerfile          # bundles toolbox binary into a Caddy image
├── fly.toml            # Fly machine config
├── .gitignore
└── README.md           # quickstart for new contributors
```

The `docs/` tree already exists (architecture, ADR, this file).

### `tools.yaml`

```yaml
# fred-mcp tools.yaml
# Reference: https://github.com/googleapis/mcp-toolbox

# ──────────────────────────────────────────────────────────────────────
# Source: the Fred MariaDB instance. All credentials come from env vars,
# which are set as Fly secrets.
# ──────────────────────────────────────────────────────────────────────
sources:
  fred:
    kind: mysql
    host: ${MYSQL_HOST}
    port: ${MYSQL_PORT:3306}
    database: ${MYSQL_DATABASE}
    user: ${MYSQL_USER}
    password: ${MYSQL_PASSWORD}
    queryTimeout: 30s

# ──────────────────────────────────────────────────────────────────────
# Tools
# ──────────────────────────────────────────────────────────────────────
tools:

  # ── Built-in introspection + escape hatch ──────────────────────────

  execute_sql:
    kind: mysql-execute-sql
    source: fred
    description: |
      Execute a single read-only SQL statement against the Fred MariaDB
      database and return the rows as JSON. Use this when the curated
      tools below do not cover the question being asked.

      Important conventions for Fred (also see the `fred_query_rules`
      MCP prompt for the full ruleset):
      - The DB user is read-only. Only SELECT statements will succeed.
      - Strategic languages: `resource_level >= 1`.
      - Use `iso_639_2` to join `language_engagements` to
        `joshua_project_data` or `pb_language_data`.
      - For all-languages-in-a-country, query `pb_language_data`.
      - For languages where unfoldingWord is engaged, query
        `language_engagements` (or the denormalized
        `master_uw_language_engagements`).
      - Use `LIKE` for language name searches; names are formatted as
        `language [code]` in `pb_language_data.languagename`.

  list_tables:
    kind: mysql-list-tables
    source: fred
    description: |
      List all user tables in the Fred database with their columns,
      constraints, indexes, and triggers as JSON. Use to discover the
      schema before writing SQL.

  get_query_plan:
    kind: mysql-get-query-plan
    source: fred
    description: |
      Run EXPLAIN against a SQL statement and return the query plan as
      JSON. Use to validate that an `execute_sql` query will be efficient
      before running it on a large table.

  # ── Curated domain tools ───────────────────────────────────────────

  find_language_engagements:
    kind: mysql-sql
    source: fred
    description: |
      Find unfoldingWord language engagements matching a language and/or
      country. Returns engagement details joined with organization name,
      language name, and country name.

      A "language engagement" is the intersection of a language, a
      country, and a partner translation organization where unfoldingWord
      has work in progress. If the user asks about ALL languages in a
      country (not just ones uW is working on), do NOT use this tool —
      use `country_language_overview` instead.

      Both parameters accept NULL to widen the search; at least one must
      be provided.
    statement: |
      SELECT
        le.language_engagement_id,
        ilc.subtag_new            AS ietf_subtag,
        ilc.primary_anglicized_name AS language_name,
        ilc.iso_639_2,
        c.alpha_3_code            AS country_code,
        c.english_short_name      AS country_name,
        o.name                    AS organization_name,
        o.org_type,
        le.sensitivity,
        le.population,
        le.partner_goal,
        le.language_cluster
      FROM language_engagements le
      JOIN ietf_languages_codes ilc ON ilc.ietf_id = le.ietf_id
      JOIN countries c              ON c.alpha_3_code = le.country_id
      JOIN organizations o          ON o.org_id = le.translation_organization_id
      WHERE (? IS NULL OR ilc.subtag_new LIKE CONCAT('%', ?, '%')
                       OR ilc.primary_anglicized_name LIKE CONCAT('%', ?, '%'))
        AND (? IS NULL OR c.alpha_3_code = ? OR c.english_short_name LIKE CONCAT('%', ?, '%'))
      ORDER BY ilc.primary_anglicized_name, c.english_short_name
      LIMIT 200;
    parameters:
      - name: language_query
        type: string
        description: |
          Language identifier — IETF subtag (e.g., "hi", "swh") or a
          fragment of the anglicized language name (e.g., "Hindi").
          Pass null to match any language.
      - name: country_query
        type: string
        description: |
          Country identifier — ISO alpha-3 code (e.g., "IND", "USA")
          or a fragment of the English country name (e.g., "India").
          Pass null to match any country.

  list_translation_products_for_engagement:
    kind: mysql-sql
    source: fred
    description: |
      List all translation products (scripture portions, NT, full Bible,
      etc.) for a given language engagement, with status and dates.
      Use after `find_language_engagements` returns a
      `language_engagement_id` the user wants to drill into.

      In `uw_translation_products`:
      - `resource_package` = type of product translated.
      - `scriptural_association` = the actual biblical book (USE THIS,
        not `scripture_text_name`).
      - `product_status` is one of: "Not Scheduled", "Inactive",
        "In Progress", "Completed".
    statement: |
      SELECT
        id,
        resource_package,
        scriptural_association,
        scripture_text_name,
        translation_type,
        resource_format,
        product_status,
        start_date,
        published_date,
        completed_date
      FROM uw_translation_products
      WHERE language_engagement_id = ?
      ORDER BY published_date DESC, start_date DESC;
    parameters:
      - name: language_engagement_id
        type: integer
        description: The language_engagement_id (from find_language_engagements).

  search_organizations:
    kind: mysql-sql
    source: fred
    description: |
      Search unfoldingWord partner organizations by a name fragment.
      Returns org metadata including type, sensitivity, and Progress
      Bible org id linkage.
    statement: |
      SELECT
        org_id,
        name,
        org_type,
        sensitivity,
        data_usage_permission,
        cbbt_spectrum,
        org_status,
        pb_org_id
      FROM organizations
      WHERE name LIKE CONCAT('%', ?, '%')
        AND COALESCE(marked_delete, 0) = 0
      ORDER BY name
      LIMIT 50;
    parameters:
      - name: name_fragment
        type: string
        description: A substring of the organization's name (case-insensitive).

  country_language_overview:
    kind: mysql-sql
    source: fred
    description: |
      Return all languages spoken in a country (Progress Bible's view,
      not just languages unfoldingWord is engaged with) with translation
      status, AAG goal, and chapter progress.

      Use this when the user asks about ALL languages in a country,
      population coverage, or All Access Goal status. Do NOT use this for
      questions specifically about unfoldingWord engagements — use
      `find_language_engagements` for those.
    statement: |
      SELECT
        languagename,
        languagecode,
        iso_639_2 := NULL AS iso_639_2_placeholder,
        country,
        countrycode,
        firstlanguageusers,
        completedscripture,
        latestpublicationyear,
        activetranslation,
        allaccessstatus,
        allaccessgoal,
        allaccessgoalmet,
        chaptergoal,
        chapterscompleted,
        chapterstogoal,
        onthelist
      FROM pb_language_data
      WHERE countrycode = ? OR country LIKE CONCAT('%', ?, '%')
      ORDER BY firstlanguageusers DESC
      LIMIT 500;
    parameters:
      - name: country_code
        type: string
        description: ISO alpha-2 country code (e.g., "IN", "US"), or null.
      - name: country_name_fragment
        type: string
        description: Substring of the country name (e.g., "India"), or null.

  engagement_metadata_notes:
    kind: mysql-sql
    source: fred
    description: |
      Return all free-text notes / communication log entries attached to
      a language engagement, most recent first. Useful for answering
      questions about engagement history, status changes, and decisions.
    statement: |
      SELECT
        metadata_id,
        communication_date,
        created_by,
        note
      FROM language_engagement_metadata
      WHERE language_engagement_id = ?
      ORDER BY communication_date DESC, metadata_id DESC
      LIMIT 200;
    parameters:
      - name: language_engagement_id
        type: integer
        description: The language_engagement_id to fetch notes for.

  recent_training_events:
    kind: mysql-sql
    source: fred
    description: |
      List recent unfoldingWord training events, optionally filtered by
      country. Includes participant counts and the generated
      `training_man_hours` column (= event_hours × total_facilitators).
    statement: |
      SELECT
        te.training_event_id,
        te.event_name,
        te.start_date,
        te.trip_duration_days,
        te.training_event_hours,
        te.training_man_hours,
        te.training_category,
        te.zoom_in_person,
        c.english_short_name AS country_name,
        c.alpha_3_code       AS country_code,
        o.name               AS organization_name,
        te.participants_present,
        te.translator_participants_present,
        te.trainer_participants_present
      FROM training_events te
      LEFT JOIN countries     c ON c.alpha_3_code = te.country_id
      LEFT JOIN organizations o ON o.org_id      = te.org_id
      WHERE (? IS NULL OR te.country_id = ? OR c.english_short_name LIKE CONCAT('%', ?, '%'))
        AND te.start_date >= COALESCE(?, DATE_SUB(CURDATE(), INTERVAL 1 YEAR))
      ORDER BY te.start_date DESC
      LIMIT 200;
    parameters:
      - name: country_query
        type: string
        description: |
          Country alpha-3 code (e.g., "IND") or country name fragment
          (e.g., "India"). Pass null to include all countries.
      - name: since_date
        type: string
        description: |
          ISO date (YYYY-MM-DD). Only return events on or after this
          date. Pass null to default to the last 12 months.

# ──────────────────────────────────────────────────────────────────────
# Toolsets
# ──────────────────────────────────────────────────────────────────────
toolsets:
  fred:
    - execute_sql
    - list_tables
    - get_query_plan
    - find_language_engagements
    - list_translation_products_for_engagement
    - search_organizations
    - country_language_overview
    - engagement_metadata_notes
    - recent_training_events

# ──────────────────────────────────────────────────────────────────────
# Prompts
# ──────────────────────────────────────────────────────────────────────
prompts:
  fred_query_rules:
    kind: custom
    description: |
      The unfoldingWord domain conventions and field-level rules for
      querying the Fred database correctly. Pull this prompt before
      writing SQL via execute_sql or interpreting results from the
      curated tools.
    messages:
      - role: user
        content: |
          You are answering questions about unfoldingWord's translation
          tracking data ("Fred"). Follow these rules strictly.

          # Strategic Language
          A language is Strategic iff `resource_level >= 1`.

          # All Access Goals (AAG)
          - allaccessgoal ∈ {"Two Bibles", "Bible", "NT / 260 Chapters",
            "25 Chapters"}
          - allaccessgoalmet ∈ {"Yes", "No", "Not Shown",
            "Not on All Access Goals List"}
          - allaccessstatus is the canonical AAG status field. Values:
            "Goal Met in the language",
            "Not on All Access Goals List",
            "Translation in Progress",
            "Translation Not Started",
            "Goal Met - Scripture accessed via second language",
            "Not Shown".
          - `onthelist` indicates ETEN funding eligibility.

          # Joins
          - Joining `language_engagements` to `joshua_project_data` or
            `pb_language_data`: ALWAYS join on `iso_639_2`. Never use
            other language code fields.
          - For `joshua_project_data` → AAG via `pb_language_data`:
            join `joshua_project_data.rol3` to `pb_language_data.languagecode`.
          - For `joshua_project_data` → engagements: join
            `joshua_project_data.rol3` to `language_engagements`'s
            `iso_639_2` (via `ietf_languages_codes`).

          # Status fields
          - project_status ∈ {"Completed", "Active", "Inactive"}.
          - product_status ∈ {"Not Scheduled", "Inactive", "In Progress",
            "Completed"}.

          # Language engagements vs Progress Bible
          - "All languages in a country" → query `pb_language_data`.
          - "Languages where unfoldingWord is engaged" → query
            `language_engagements` (or `master_uw_language_engagements`).

          # Translation products
          - `resource_package` = type of product translated.
          - `scriptural_association` = the actual biblical book.
            NEVER use `scripture_text_name` for the book — use
            `scriptural_association`.

          # Time-based queries
          Several tables use MariaDB system versioning. For
          rate-of-change or point-in-time queries, use `FOR SYSTEM_TIME`:

              SELECT *,
                CONVERT_TZ(row_start, 'UTC', 'America/New_York') AS row_start_local,
                CONVERT_TZ(row_end,   'UTC', 'America/New_York') AS row_end_local
              FROM pb_language_data
              FOR SYSTEM_TIME FROM '2025-05-01 00:00:00' TO NOW()
              ORDER BY row_start_local DESC;

          # Unreached People Groups (UPGs)
          - UPG = people group with `jpscale <= 1`.
          - Least Reached People Group = `jpscale <= 3`.
          - Always use `joshua_project_data` for UPG and population
            questions.
          - Do NOT filter on `leastreached`; ALWAYS use `jpscale`.
          - "How many UPGs?" means COUNT DISTINCT people groups, not
            languages — use `peopleid3rog3` for the people-group id.
          - Use `pb_language_data.country` for geography unless JP
            geography is explicitly requested.
          - `jpscale` ∈ [0, 5]. `frontier` ∈ {"Y", "N"}.

          # Name searches
          When searching language names, use `LIKE`. The
          `pb_language_data.languagename` column has format
          `language [code]`. Same convention applies to
          `pb_all_translation_projects.Language_Name`.
```

> **Note on YAML structure:** the toolbox config supports both the
> top-level `sources:` / `tools:` / `toolsets:` / `prompts:` mapping form
> AND the multi-document `kind: source` / `kind: tool` etc. form. The
> mapping form above is more compact; if it doesn't parse, switch to the
> multi-document form (see prebuilt configs in
> `mcp-toolbox/internal/prebuiltconfigs/tools/postgres.yaml`).

### `Caddyfile`

```caddy
{
    # Disable Caddy's admin API (we don't need it; reduces attack surface).
    admin off
    # Log to stdout so Fly captures it.
    log {
        output stdout
        format console
    }
}

:8080 {
    # Health check passthrough — no auth required, returns 200.
    # Useful for Fly health checks and external uptime monitoring.
    handle /healthz {
        respond "ok" 200
    }

    # Everything else requires the bearer token.
    @authorized header Authorization "Bearer {env.FRED_MCP_TOKEN}"
    handle @authorized {
        reverse_proxy 127.0.0.1:5000
    }

    # Default: 401 with no body, no hint about what's behind us.
    handle {
        respond 401
    }
}
```

> The `@authorized` matcher uses Caddy's `env.*` placeholder, which
> reads from the process environment at request time. The
> `FRED_MCP_TOKEN` env var is set by Fly from the secret of the same
> name.

### `entrypoint.sh`

```sh
#!/bin/sh
set -e

# Start toolbox bound to localhost only — Caddy is the public face.
/usr/local/bin/toolbox \
    --tools-file /app/tools.yaml \
    --address 127.0.0.1 \
    --port 5000 \
    &
TOOLBOX_PID=$!

# Forward signals so Fly can stop both processes cleanly.
trap "kill $TOOLBOX_PID 2>/dev/null; exit 0" TERM INT

# Caddy in the foreground keeps the container alive.
exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
```

> Verify the toolbox CLI flag for the config file against `1.1.0`. It
> may be `--tools-file`, `--config`, or `--tools_file`. Fix during
> the local smoke test (step 5).

### `Dockerfile`

```dockerfile
# Pin a specific toolbox version. Update intentionally.
ARG TOOLBOX_VERSION=1.1.0

# Stage 1: pull the toolbox binary out of the official image.
FROM us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:${TOOLBOX_VERSION} AS toolbox

# Stage 2: Caddy as the runtime, with toolbox bundled in.
FROM caddy:2-alpine

COPY --from=toolbox /toolbox /usr/local/bin/toolbox
COPY tools.yaml      /app/tools.yaml
COPY Caddyfile       /etc/caddy/Caddyfile
COPY entrypoint.sh   /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
```

> The toolbox image is a [distroless][distroless] base with no shell,
> so we can't install Caddy *into* it. Instead we use Caddy's image as
> the runtime and copy the toolbox binary across. The toolbox binary
> is a static Go executable so it runs fine on Alpine.

[distroless]: https://github.com/GoogleContainerTools/distroless

### `fly.toml`

```toml
app = "fred-mcp"
primary_region = "iad"  # pick the region closest to Fred

[build]
  dockerfile = "Dockerfile"

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0

[[vm]]
  cpu_kind = "shared"
  cpus = 1
  memory_mb = 512

# Secrets are NOT defined here — set them with `fly secrets set`:
#   MYSQL_HOST, MYSQL_PORT, MYSQL_DATABASE, MYSQL_USER, MYSQL_PASSWORD,
#   FRED_MCP_TOKEN

[[http_service.checks]]
  grace_period = "10s"
  interval = "30s"
  method = "get"
  path = "/healthz"
  protocol = "http"
  timeout = "5s"
```

### `.gitignore`

```
.DS_Store
.envrc
.env
.env.*
*.log
node_modules/
```

### `README.md` (sketch — final wording at author's discretion)

```markdown
# fred-mcp

MCP server exposing the unfoldingWord Fred database to MCP clients
(Claude, ChatGPT, Cursor, custom agents).

The server is `mcp-toolbox` configured by [`tools.yaml`](./tools.yaml).
We don't write application code; we maintain YAML.

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

Where `.env.local` contains:

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
```

## Implementation steps

1. **Provision the read-only DB user** (see Pre-reqs §1) on the staging
   Fred instance. Verify with `SHOW GRANTS`.
2. **Create the Fly app**: `fly launch --no-deploy --name fred-mcp`.
3. **Set Fly secrets** with the staging DB credentials.
4. **Add the files** in this issue (`tools.yaml`, `Dockerfile`,
   `fly.toml`, `.gitignore`, `README.md`).
5. **Local smoke-test** the full image before deploying:
   ```sh
   docker build -t fred-mcp:local .
   docker run --rm -p 8080:8080 --env-file .env.local fred-mcp:local
   ```
   Verify, in a second terminal:
   - `curl -i http://localhost:8080/healthz` → `200 ok`
   - `curl -i http://localhost:8080/mcp` → `401`
   - `curl -i -H "Authorization: Bearer $FRED_MCP_TOKEN" http://localhost:8080/mcp`
     → toolbox response listing all 9 tools and the `fred_query_rules`
     prompt.

   This is also where the YAML version-drift issues from the callout
   at the top of this issue will surface. If toolbox fails to start,
   read its logs (`docker logs <id>`), adjust `tools.yaml` structure
   and/or the `--tools-file` flag in `entrypoint.sh`, rebuild, retry.
6. **Deploy**: `fly deploy`.
7. **Connect Claude Desktop** and run the smoke tests in the next
   section.
8. **Document the public URL** in the README.
9. **Commit and push.**

## Acceptance criteria

- [ ] `fred_mcp_ro` MariaDB user exists with SELECT-only grants;
      `SHOW GRANTS` output is in this issue's resolution comment.
- [ ] `fly status` shows `fred-mcp` healthy.
- [ ] `FRED_MCP_TOKEN` is set as a Fly secret and stored in
      1Password / shared secret store.
- [ ] `curl https://fred-mcp.fly.dev/healthz` returns `200 ok`
      without auth.
- [ ] `curl https://fred-mcp.fly.dev/mcp` (no auth header) returns
      `401`.
- [ ] `curl -H "Authorization: Bearer $TOKEN" https://fred-mcp.fly.dev/mcp`
      returns a tool list including all 9 tools and the
      `fred_query_rules` prompt.
- [ ] `curl -H "Authorization: Bearer wrong-token" https://fred-mcp.fly.dev/mcp`
      returns `401` (proves the token is actually being checked, not
      just any header).
- [ ] From Claude Desktop, the following queries each return a sensible
      answer (paste transcripts into the resolution comment):
  - "What language engagements does unfoldingWord have in India?"
    *(expects `find_language_engagements` to be called)*
  - "Show me the recent translation products for engagement 123."
    *(expects `list_translation_products_for_engagement` after a lookup)*
  - "Which All Access Goal languages in Nigeria still need a Bible?"
    *(expects `country_language_overview` + AAG filtering, possibly
     guided by the `fred_query_rules` prompt)*
  - "How many UPGs are there in Pakistan?"
    *(expects `execute_sql` against `joshua_project_data` with
     `jpscale <= 1` per the prompt rules)*
- [ ] Attempting `INSERT INTO organizations ...` via `execute_sql` fails
      with a permissions error (proves the read-only role).

## Risks & known gaps

- **Shared bearer token, not per-user identity.** All consumers use the
  same token, so we can't tell from logs *which* consumer made a given
  query — only that it was an authorized one. Acceptable for a small
  set of trusted consumers; revisit if the consumer list grows or we
  need per-user audit. Tracked as: *follow-up issue TBD — add OIDC
  with per-user identity*.
- **Token rotation is manual.** Rotating means changing the Fly secret
  + updating every consumer's MCP config. No automation in v1.
- **Single environment.** No staging/prod split. Tracked as: *follow-up
  issue TBD*.
- **Manual deploys.** No CI. Tracked as: *follow-up issue TBD — port
  fia-mcp's GitHub Actions structure for `tools.yaml` linting +
  `fly deploy`*.
- **Toolbox YAML schema drift** — see callout at top of this issue.
  Will surface during the local smoke test (step 5); allow ~30 minutes
  to mechanically convert structure to match the pinned `1.1.0`
  release.
- **Fred network reachability.** If Fred is private and Fly egress isn't
  allowlisted, deploy will succeed but tools will time out. Confirm in
  pre-reqs §3.
- **`country_language_overview` SQL is suspicious.** The
  `iso_639_2 := NULL AS iso_639_2_placeholder` line is a placeholder for
  a column that doesn't exist on `pb_language_data` — the table doesn't
  have an `iso_639_2` column directly. Either remove the line or join
  through `ietf_languages_codes` to surface it. Fix during smoke-testing.

## References

- [`docs/architecture.md`](../architecture.md)
- [`docs/decisions/0001-toolbox-as-mcp-server.md`](../decisions/0001-toolbox-as-mcp-server.md)
- [`docs/repo-survey.md`](../repo-survey.md)
- [mcp-toolbox README][toolbox]
- [mcp-toolbox prebuilt MySQL config][prebuilt-mysql] (reference for tool types)
- Fred schema: `fred-zulip-bot/context/DDLs.rtf`
- Fred query rules source: `fred-zulip-bot/context/system_prompt_rules.txt`

[toolbox]: https://github.com/googleapis/mcp-toolbox
[prebuilt-mysql]: https://github.com/googleapis/mcp-toolbox/blob/main/internal/prebuiltconfigs/tools/mysql.yaml
