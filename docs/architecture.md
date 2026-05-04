# fred-mcp architecture

## What this repo is

`fred-mcp` is an MCP (Model Context Protocol) server that exposes the
unfoldingWord "Fred" MariaDB database to MCP-compatible clients — Claude
Desktop, ChatGPT, Cursor, custom agents, etc. The goal is that any LLM
client speaking MCP can ask questions about Fred's translation data
without needing custom integration code per client.

## The big idea: we don't write the MCP server, we configure one

The MCP server itself is [Google's `mcp-toolbox`][toolbox] running as a
container on Fly.io. We don't ship Go or TypeScript code. We ship a
`tools.yaml` file that declares:

- **Sources** — how to connect to Fred (host, credentials)
- **Tools** — named operations the LLM can call (parameterized SQL queries
  or built-in introspection tools)
- **Toolsets** — bundles of tools exposed to a particular client
- **Prompts** — reusable instructions (e.g., the rules from
  `fred-zulip-bot`'s system prompt) that an MCP client can pull in
- **Auth** — optional OIDC enforcement on tools

The toolbox runtime reads the YAML on startup, opens a connection pool to
Fred, and exposes everything over MCP (HTTP/SSE). To an MCP client, this
is indistinguishable from a hand-rolled MCP server.

## Mental model

```
┌──────────────────────┐     MCP over HTTP/SSE     ┌──────────────────────┐
│  Claude / ChatGPT /  │ ◄────────────────────────►│  toolbox container   │
│  Cursor / agent      │                            │  on Fly.io           │
└──────────────────────┘                            │                      │
                                                    │  reads tools.yaml    │
                                                    │  pgx-style pool      │
                                                    │  ↓                   │
                                                    └──────────┬───────────┘
                                                               │
                                                               ▼
                                                    ┌──────────────────────┐
                                                    │  Fred MariaDB        │
                                                    │  (existing instance) │
                                                    └──────────────────────┘
```

## Three flavors of tools

| Flavor | YAML `type` | Purpose |
|---|---|---|
| Prebuilt operational | `mysql-list-tables`, `mysql-execute-sql`, `mysql-get-query-plan`, etc. | Schema introspection + free-form SQL escape hatch. ~25 of these ship out of the box for MySQL/MariaDB. |
| Custom parameterized SQL | `mysql-sql` | Our domain queries. SQL with `?` placeholders bound to declared, typed parameters. Injection-safe by construction. |
| Templated SQL | `mysql-sql` with `templateParameters` | SQL where part of the statement is itself substituted (e.g., wrapping `EXPLAIN`). Used sparingly. |

## Why descriptions matter

Each tool's `description` field becomes the LLM's contract for that tool —
it's what shows up in the LLM's tool catalog. We treat descriptions as
prompt engineering, not docstrings:

- State exactly when to call the tool ("use this when the user asks about
  language engagements in a specific country")
- State what the tool returns and in what shape
- Include any domain rules the LLM needs (e.g., "join on `iso_639_2`, never
  on `subtag_new`")

This is most of the work. SQL is the easy part.

## The escape hatch

The `execute_sql` tool (built-in `mysql-execute-sql`) lets the LLM run any
SQL it wants. This is intentional and important — the curated tools won't
cover every question users will ask. The escape hatch makes the system
useful when curated tools fall short.

The escape hatch is safe **only** because the MariaDB user we provide
toolbox is read-only with `SELECT`-only grants on the relevant tables.
Provisioning that user is part of the bootstrap.

See [the bootstrap issue](./issues/001-toolbox-mcp-bootstrap.md) for the
exact `GRANT` statements.

## What the system intentionally does not do (in v1)

- **No NL2SQL layer.** Toolbox doesn't generate SQL from prose; the LLM
  picks tools and supplies args, or writes SQL itself via `execute_sql`.
- **No response shaping.** Whatever the SQL returns is what the LLM sees.
  No server-side joins across tools, no post-processing.
- **No per-tool auth.** v1 has either no auth or a single shared bearer
  token (TBD in the bootstrap issue). Per-user OIDC + auth-bound query
  parameters is a v2 concern.
- **No write tools.** The DB user is read-only.
- **No prompts beyond the Fred rules prompt.** Toolbox supports MCP
  prompts; we ship one (the language-data rules) and stop there.

## What would push us off this architecture

We picked this architecture (vs. writing our own TS or Go MCP server)
because almost all of fred-zulip-bot's value is parameterized SQL with
good descriptions. If any of these become true, we'd revisit
[ADR 0001](./decisions/0001-toolbox-as-mcp-server.md):

1. We need a tool that calls a non-SQL data source (e.g., a REST API or
   another MCP server) and joins the result with SQL output.
2. We need server-side response shaping that SQL alone can't express.
3. We need fine-grained per-user auth and the OIDC story doesn't fit.
4. The `tools.yaml` grows past ~50 tools and YAML maintenance becomes the
   bottleneck.

## Repository layout

```
fred-mcp/
├── tools.yaml              # the MCP server, declaratively
├── Dockerfile              # wraps the official toolbox image
├── fly.toml                # Fly machine config
├── README.md               # quickstart for new contributors
└── docs/
    ├── architecture.md     # this file
    ├── repo-survey.md      # what we learned from related repos
    ├── decisions/
    │   └── 0001-toolbox-as-mcp-server.md
    └── issues/
        └── 001-toolbox-mcp-bootstrap.md
```

[toolbox]: https://github.com/googleapis/mcp-toolbox
