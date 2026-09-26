# SchemaLens

**See your PostgreSQL schema as a live relationship graph, with the
structural problems that will slow it down.**

When you write SQL you usually can't tell what it will cost: whether it
scans a whole table or quietly pushes CPU up. Tools like pganalyze, Datadog
and PMM tell you *after* the slow query reaches production. SchemaLens aims to
tell you **while you are writing it**. The first step, and what this
repository does today, is to understand the database: which tables exist, how
big they are, how they relate, and what's structurally wrong. It's shown as
an interactive graph that follows the database as you migrate it.

![SchemaLens showing the demo shop database](docs/screenshot.png)

- **Live.** Run a migration and the graph updates within seconds. New tables
  appear next to the tables they reference, and nothing else moves.
- **Read-only by design.** Every session runs with
  `default_transaction_read_only = on` and a statement timeout. Suggested fixes
  are text for you to copy; SchemaLens never runs them.
- **Connect from the page.** Paste a connection URL or type host, user and
  password, and the graph is drawn. Switch databases without restarting.
- **Saved graphs.** Save a graph with its layout and open it again later,
  without the database. The password is never saved.
- **One binary.** The UI is built in, works offline, and needs no Node or CDN.

## Quickstart

You need Go 1.25+ and Docker.

```sh
git clone https://github.com/sanat-19/schema-lens
cd schema-lens
make demo
```

`make demo` starts Postgres 16 with a demo shop database (17 tables, one of
each problem SchemaLens looks for) and opens the UI. To watch the live
updates, keep the page open and change the schema in another terminal:

```sh
docker compose exec postgres psql -U schemalens -d shop \
  -c "CREATE TABLE wishlists (id bigserial PRIMARY KEY, user_id bigint REFERENCES users(id))"
```

## Connect from the browser

Run `schemalens serve` with no flags and open http://127.0.0.1:8080. The page
asks for a database: paste a `postgres://` URL, or enter host, port,
database, user, password and SSL mode. Optionally, list which schemas to read.
The graph is drawn as soon as the schema has been read, and it stays live.
Use **Switch** to connect to another database. If a connection fails (say, a
wrong password), the error is shown and the current graph stays.

The password is used for the connection and kept in memory only. It is
never written to disk, never logged, and never sent back to the page.

![Connect screen with a saved graph](docs/screenshot-connect.png)

## Saved graphs

**Save** in the toolbar keeps the current graph: the schema at that moment
(tables, relations and findings), where each table sits on screen, and which
database it came from. Saved graphs are listed on the Connect screen:

- **Open** shows it exactly as it was saved, without needing the database.
- **Reconnect** fills in the connection form from where it came from. You
  type the password, since it was never saved.
- **Delete** removes it.

They're JSON files, one per graph, in `~/.config/schemalens/graphs` (or the
equivalent on macOS and Windows), readable only by you. Use `--data-dir` to
keep them somewhere else, e.g. next to a project.

## Usage

```
schemalens serve    [--addr 127.0.0.1:8080] [--open] [--data-dir dir]
schemalens serve    --dsn <url> [--schemas public,billing] [--addr ...] [--open]
schemalens serve    --from snapshot.json
schemalens snapshot --dsn <url> [--schemas ...] -o snapshot.json
schemalens export   --dsn <url> [--schemas ...] --format json|mermaid [-o file]
```

| Flag | Meaning |
|---|---|
| `--dsn` | Connection string. Defaults to `$DATABASE_URL`. With neither, `serve` opens on the Connect screen. The password is never printed. |
| `--schemas` | Schemas to read. Defaults to every non-system schema. |
| `--addr` | Where to serve the UI. Defaults to `127.0.0.1:8080`, local only, because it shows your schema. |
| `--open` | Open the UI in your browser. |
| `--from` | Show a saved snapshot instead of a live database. |
| `--data-dir` | Where saved graphs are kept (default: your config directory). |
| `--watch-interval` | How often to check for schema changes (default `2s`). |
| `--stats-interval` | How often to refresh row counts, sizes and index usage (default `30s`). |

Exit codes: `0` success, `1` bad command line, `2` couldn't connect or read
the database.

**Snapshots** let you look at a schema later without access to the
database, on a plane or when a colleague sends you theirs:

```sh
schemalens snapshot --dsn "$PROD_READONLY_URL" -o prod.json
schemalens serve --from prod.json
```

**Mermaid** output renders straight from a Markdown code block on GitHub and
GitLab. It's also available live at `/api/export/mermaid` while `serve` is
running.

## The UI

- **Left:** the database and whether it's live, a table filter (press `/`),
  every table with its size, and the findings grouped by severity. Opening a
  finding jumps to its table and shows SQL to copy.
- **Centre:** each table as an ER card. 🔑 is a primary key, 🔗 a foreign key,
  `U` unique, and `?` or dimmed text a nullable column. Edges run from the
  table holding the FK to the table it references, labelled `N:1` / `1:1`
  (`0..1` when the FK is nullable). Dashed edges are relations guessed from
  column names. A red or orange border means a high or medium finding.
  Tables in schemas other than `public` are tinted with their schema's
  colour (say `billing` red and `cart` blue), shown in the legend and the
  sidebar, so tables that belong together stand out.
  Click a table to light up its neighbours, and double-click or press Esc to
  reset. For big schemas, "Names only" keeps 250 tables laid out in under
  half a second.
- **Right:** the selected table's columns, indexes (size, scan count, and
  tags for unused, redundant or duplicate), what it references, what
  references it, and its findings.

It follows your OS's light or dark theme.

![Details panel in dark mode](docs/screenshot-details-dark.png)

## What SchemaLens finds

| Finding | Severity | Why it matters |
|---|---|---|
| `missing_fk_index` | high if the child table has over 10k rows, else medium | Postgres doesn't index the referencing side of an FK. Joins to the parent, and every delete or key update on the parent, scan the whole child table. |
| `fk_type_mismatch` | medium | The FK column's type differs from the key it references, e.g. `integer` → `bigint`. Comparisons need a cast, and for integers the child column overflows once parent ids pass 2³¹. |
| `no_primary_key` | medium | Nothing stops duplicate rows, ORMs can't safely target one row, and logical replication can't replicate UPDATEs and DELETEs. |
| `duplicate_index` | medium | Two indexes with the same method, columns and predicate. Every write updates both for no benefit. |
| `redundant_index` | low | `(a)` when `(a, b)` exists. The longer index serves the same lookups, so the short one only costs writes and disk. |
| `unused_index` | low | Over 1 MB and never scanned since stats were reset. Indexes that back a constraint or an FK are never flagged. |
| `inferred_relation` | low | A column like `product_id` clearly points at `products` but has no FK, so orphaned rows can creep in. |

Every finding comes with SQL you can copy, written to avoid long locks where
Postgres allows it (`CREATE INDEX CONCURRENTLY`, `ADD CONSTRAINT ... NOT VALID`
then `VALIDATE`), and with a note when that isn't possible, such as on
partitioned tables.

## How it works

```mermaid
flowchart LR
    PG[(PostgreSQL)] -- pg_catalog, read-only --> I[postgres<br/>Introspect]
    PG -- fingerprint every 2s --> W[server<br/>Watcher]
    W -- changed? --> I
    I --> M[schema<br/>model]
    M --> G[graph<br/>relations]
    G --> A[analyze<br/>findings]
    A --> H[server<br/>Hub]
    H -- /api/schema --> UI[web UI<br/>Cytoscape + dagre]
    H -- /api/events SSE --> UI
    M -. snapshot JSON .-> H
    M --> R[render<br/>Mermaid]
```

| Package | Job |
|---|---|
| `internal/schema` | The database-agnostic model: tables, columns, keys, indexes, relations, findings. Everything else reads it. |
| `internal/postgres` | The only package that talks to Postgres. It reads `pg_catalog` in one read-only transaction, one batched query per kind of object, and computes the change fingerprint. |
| `internal/graph` | Relations from FKs (cardinality, optionality) and relations guessed from column names. |
| `internal/analyze` | The seven checks above. These are pure functions over the model, unit-tested without a database. |
| `internal/render` | Mermaid `erDiagram` export. |
| `internal/server` | The HTTP API, the Server-Sent Events stream, the watcher that keeps the schema current, and switching between databases and saved graphs. |
| `internal/store` | Saved graphs on disk: one JSON file each, written atomically, readable only by you. |
| `web/` | The UI: plain HTML, CSS and JS, embedded in the binary. |

**Staying live without writing to your database:** Postgres can push schema
changes through event triggers, but creating one is a write that needs
superuser. So SchemaLens asks instead. A single query hashes the structural
catalog (tables, columns, types, constraints, indexes, comments), which takes
about 2 ms, and only when the hash changes does it read the full schema and
tell the browser. The UI then patches the graph rather than redrawing it.

See [`DEVELOPMENT_PLAN.md`](DEVELOPMENT_PLAN.md) for how the project was built
step by step, and [`DECISIONS.md`](DECISIONS.md) for the calls made along the way.

**Keeping the connect endpoint safe:** `POST /api/connect` makes SchemaLens
open database connections, so other websites must not be able to call it
through your browser. The server only accepts JSON POSTs (a cross-site JSON
POST needs a CORS preflight, which it never allows), rejects foreign
`Origin` headers, and, when listening on localhost, rejects requests whose
`Host` isn't localhost (which stops DNS rebinding).

## Development

```sh
make db-up             # Postgres 16 + demo schema on localhost:5433
make test              # unit tests, no database needed
make test-integration  # plus the tests that need the demo database
make lint              # go vet + staticcheck
make db-reset          # reload the demo schema from scratch
```

CI runs all of this against a Postgres 16 service container on every push.

## Roadmap

- **Phase 2: query analysis.** Read `pg_stat_statements`, rank queries by
  total and mean time, run `EXPLAIN` on them, catch sequential scans on big
  tables and bad row estimates, and colour the hot tables and edges on the
  graph. It builds on the row estimates, index usage stats and live loop that
  exist today.
- **Phase 3: shift-left.** Find the SQL in Go code (raw strings, sqlc, GORM),
  `EXPLAIN` it against a shadow database with the production schema *and
  production planner statistics* (not production data), and comment on the
  pull request with the estimated cost and a suggested fix.
- **Later:** MySQL, as a second implementation of the `Introspector`
  interface.
