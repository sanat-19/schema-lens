# SchemaLens — Development Plan

This file is the story of how SchemaLens gets built, one step at a time.

For every step it answers the same questions:

- **Why** — what problem this step solves, and why it comes at this point.
- **What we need** — the requirements we gathered before writing code.
- **Packages** — which Go or frontend packages we use, and why those.
- **How it helps** — what the next steps can do because this one exists.
- **Done when** — how we know the step is finished.

We do not jump ahead. Each step stands on the one before it. When a step is
finished, its box gets ticked and any surprises go into `DECISIONS.md`.

---

## The big picture: why SchemaLens exists

When we write SQL, we usually can't tell what it will cost. Will it scan a
whole table? Will it push CPU up? Tools like pganalyze or Datadog tell us
*after* the slow query hits production.

The long-term goal is to tell developers the cost of a query **while they are
writing it**. But you can't reason about a query without first understanding
the database it runs against: which tables exist, how big they are, how they
connect, and which indexes they have.

So Phase 1 is: **understand the schema, show it as a live graph, and point out
the structural problems.** Everything later (query analysis, PR comments) is
built on top of what we make here.

The road looks like this:

```
 connect ──► which tables? ──► columns ──► keys & FKs ──► indexes
                                                            │
      ┌─────────────────────────────────────────────────────┘
      ▼
 dump as JSON ──► relationships ──► guessed relationships ──► findings
                                                                 │
      ┌──────────────────────────────────────────────────────────┘
      ▼
 mermaid / snapshot ──► watch for changes ──► HTTP + live events ──► UI ──► tests, CI, README
```

---

## Step 0 — Project setup and a database to look at  `[x]`

**Why.** We can't build a schema reader without a schema to read. Before any
Go code, we need a real Postgres with a realistic schema, including the
mistakes we want SchemaLens to catch later.

**What we need.**
- A Go module.
- Postgres 16 running locally with one command.
- `testdata/sample_schema.sql`: an e-commerce schema (~17 tables in `public`
  and `billing`) with deliberate problems:
  - `order_items.product_id` has an FK but no index
  - `orders(user_id)` is redundant next to `orders(user_id, created_at)`
  - a duplicate index
  - an `int` column pointing at a `bigint` key
  - `reviews.product_id` has no FK at all (to test guessing)
  - `audit_log` has no primary key
  - `categories.parent_id` points at itself
  - `user_profiles` is 1:1 with `users`
  - `events` is partitioned by month
- Enough generated rows (`generate_series`) that some tables pass 10k rows,
  then `ANALYZE` so the planner stats are real.

**Packages.**
- `postgres:16` Docker image, `docker compose`. The image runs any `.sql` in
  `/docker-entrypoint-initdb.d` on first start, so loading the schema is free.
- `make` for short commands (`make db-up`, `make demo`).

**How it helps.** Every later step can be tried against this database straight
away. Because the problems are planted on purpose, we already know which
findings we expect to see. That is our answer key.

**Done when.** `make db-up` starts Postgres and `psql` shows all tables with rows.

---

## Step 1 — Connect safely  `[x]`

**Why.** Before reading anything, we make sure SchemaLens *cannot* hurt the
database. People will point this tool at real databases. It must be read-only
by design, not by promise.

**What we need.**
- DSN from `--dsn` or `DATABASE_URL`.
- Every session opens with `default_transaction_read_only = on` and a 10s
  `statement_timeout`.
- The password never appears in logs or errors. We redact it.
- Clear exit codes: 0 ok, 1 bad usage, 2 can't connect or read.

**Packages.**
- `github.com/jackc/pgx/v5` + `pgxpool`. This is the most complete Postgres
  driver for Go. It understands Postgres types natively (arrays like `int2[]`
  and `oid[]` come back as Go slices, which we need for `conkey` and
  `indkey`). The pool lets the live watcher and the HTTP server share
  connections.
- Standard `flag` package for the CLI. We only have three subcommands, so a
  framework like cobra would be more code than it saves.

**How it helps.** Every other step talks to the database through this one
door, so the safety rules only have to be right in one place.

**Done when.** `schemalens export --dsn ...` connects, prints the server
version, and an attempted write inside that session fails.

---

## Step 2 — Find out which tables exist  `[x]`

**Why.** Tables are the nodes of everything we'll draw. Before columns or
relationships make sense, we need the list of tables.

**What we need.**
- Read from `pg_catalog` (not `information_schema`): it's faster, it has the
  planner stats, and it doesn't hide tables because of column privileges.
- Only ordinary and partitioned tables (`relkind IN ('r','p')`), only in the
  schemas the user picked (default: everything except system schemas).
- For each table: its schema, name, comment, row estimate, and total size.
- Partitions (`events_2026_01`, …) are folded into their parent. The parent's
  rows and size are the sum of its children. Otherwise one table shows up as
  twelve.
- `reltuples = -1` means "never analyzed", not "minus one row".

**Packages.** Only pgx from step 1.

**The model starts here.** `models/schema.go` gets its first type:
`Table`, with `ID() = "schema.name"`. This ID is the key we use everywhere,
including in the browser. The model knows nothing about Postgres, so a MySQL
reader can fill it later.

**How it helps.** From now on, every query can be run once for all tables
using `= ANY($1::oid[])`, instead of once per table. With 200 tables that's
the difference between 1 query and 200.

**Done when.** The tool prints every table with its row estimate and size,
and `events` appears once.

---

## Step 3 — Read the columns  `[x]`

**Why.** Relationships and findings are about columns: which column points
where, which one is nullable, what type it is. We need them before anything
else.

**What we need.**
- `pg_attribute` where `attnum > 0 AND NOT attisdropped`. That skips system
  columns and columns that were dropped but still leave a row behind.
- Type via `format_type()` so we get `varchar(255)`, not a type oid.
- Nullable, default (`pg_get_expr` on `pg_attrdef`), comment.
- Constraints and indexes refer to columns by *number* (`conkey = {2}`).
  We turn those numbers into names inside the SQL, by joining to
  `pg_attribute` (see `DECISIONS.md` for why not a Go map).

**How it helps.** Knowing every column's name, type and nullability is what
the next two steps build on.

**Done when.** Every table in the JSON has its columns with the right types.

---

## Step 4 — Read keys and foreign keys  `[x]`

**Why.** Foreign keys are the real relationships in the database. Primary and
unique keys tell us whether a relationship is one-to-one or many-to-one.

**What we need.**
- `pg_constraint` with `contype IN ('p','u','f')` and `conparentid = 0`, which
  skips the copies Postgres makes on every partition.
- Map `conkey`/`confkey` to names with the map from step 3.
- Decode FK actions: `a` NO ACTION, `r` RESTRICT, `c` CASCADE, `n` SET NULL,
  `d` SET DEFAULT.
- An FK can point to a schema the user didn't load. We still record it with
  correct column names, and flag it as an external reference.
- Mark columns as `IsPK`, `IsFK`, `IsUnique` so the UI can show 🔑 and 🔗.

**How it helps.** This is the raw material for step 7 (relationships), and for
most findings in step 9.

**Done when.** The JSON shows every FK in the sample, composite ones as a
single FK, and `audit_log` has no primary key.

---

## Step 5 — Read the indexes  `[x]`

**Why.** Most of the performance problems we want to find are index problems:
missing, redundant, duplicate, unused. Phase 2 (query cost) will need them too.

**What we need.**
- `pg_index` + `pg_class` + `pg_am`, LEFT JOIN `pg_stat_user_indexes` for scan
  counts.
- Only the first `indnkeyatts` entries of `indkey` are key columns. The rest
  are `INCLUDE` columns and don't count for ordering.
- `indkey = 0` means an expression, e.g. `lower(email)`. We take its text
  from `pg_get_indexdef()`.
- Unique, primary, partial (`indpred IS NOT NULL`), method (btree/gin/…),
  size, scans.

**How it helps.** Now the model has everything the findings need. This is the
last step of reading. Everything after this works on the model, not on the
database.

**Done when.** The sample's redundant and duplicate indexes are visible in
the JSON, with the right key columns.

---

## Step 6 — Checkpoint: dump it all as JSON  `[x]`

**Why.** Before building on top of the reader, we look at what it produces.
It's much easier to spot a wrong column name in a JSON file than in a graph.

**What we need.**
- `schemalens export --format json [-o file]`.
- The `Introspector` interface in `models`, with the Postgres reader
  behind it. It's the one interface we add early, because we know MySQL is
  coming.

**Packages.** `encoding/json` from the standard library.

**How it helps.** This JSON is exactly what the browser will receive later,
so the UI can be built against a saved file. It also becomes the snapshot
format in step 10.

**Done when.** We read the JSON for the sample database and it matches what
we planted in step 0.

---

## Step 7 — Find the relationships  `[x]`

**Why.** A list of foreign keys isn't a graph yet. We need edges with meaning:
which table depends on which, one-to-one or many-to-one, required or optional.

**What we need.** In `backend/pkg/graph`:
- One relation per FK constraint. A composite FK is one edge, not one per
  column.
- **One-to-one** if the FK columns exactly equal the child's primary key or one
  of its unique constraints (compared as sets). Otherwise **many-to-one**.
- **Optional** if any FK column is nullable.
- Self-references (`categories.parent_id → categories`) become loop edges.
- Only draw an edge when the target table was loaded.

**Packages.** None. This is plain Go over the model, so it's easy to unit-test.

**How it helps.** This is the graph the UI will draw. The findings in step 9
also use it, for example to know which tables point at a big parent.

**Done when.** Unit tests cover cardinality, optional, self-reference and
composite FKs, and `user_profiles → users` comes out as one-to-one.

---

## Step 8 — Guess the missing relationships  `[x]`

**Why.** Many real databases have no FK constraints at all. The app "knows"
that `reviews.product_id` points at `products`, but the database doesn't. If
we only draw real FKs, those schemas look like disconnected boxes.

**What we need.**
- For a column that isn't already an FK and is named `<x>_id` or `<x>Id`, look
  for a table called `x`, `xs`, `xes`, or `x` with `y → ies`
  (`category_id → categories`). Same schema first, then any loaded schema.
- Only if the target has a single-column PK and the types fit (int vs bigint
  is fine, int vs uuid is not).
- Mark them `Inferred: true`. They're drawn dashed so nobody mistakes a guess
  for a fact.

**How it helps.** The graph becomes useful on messy real-world databases,
and each guess becomes a finding that suggests adding the real constraint.

**Done when.** `reviews.product_id → products` is found, and tests prove we
don't guess across incompatible types or composite PKs.

---

## Step 9 — Find the problems  `[x]`

**Why.** A pretty graph is nice. Telling a developer "this will be slow, and
here's the SQL to fix it" is useful. This is where SchemaLens starts helping.

**What we need.** In `backend/pkg/analyze`, one check per kind:

| Kind | Severity | Why it matters |
|---|---|---|
| `missing_fk_index` | high if child > 10k rows, else medium | Joins and `ON DELETE` on the parent scan the whole child table |
| `no_primary_key` | medium | Breaks logical replication and ORMs, and lets duplicate rows in |
| `redundant_index` | low | Its job is done by a longer index, but every write still pays for it |
| `duplicate_index` | medium | Two identical indexes: double the write cost, zero benefit |
| `unused_index` | low | Never scanned since the last stats reset, but costs disk and writes |
| `inferred_relation` | low | The app relies on a link the database doesn't enforce |
| `fk_type_mismatch` | medium | Implicit casts can stop the planner using an index |

- Each finding has a title, one sentence on *why*, and copy-able SQL. The SQL
  is only text. SchemaLens never runs it.
- Sorted by severity, then table.

**Packages.** None. Pure Go over the model.

**How it helps.** This is the first piece of the "tell me the cost" goal.
Phase 2 will add query-based findings through the same `Finding` type.

**Done when.** Each kind has a test with a hand-made schema, including a case
where it should *not* fire, and the sample database shows all seven kinds.

---

## Step 10 — Mermaid export and snapshots  `[x]`

**Why.** People want to paste a diagram into a README or PR, and sometimes
look at a schema without database access (on a plane, or a schema from a
colleague).

**What we need.**
- `schemalens export --format mermaid` writes an `erDiagram`.
- `schemalens snapshot -o snapshot.json` saves the whole model.
- `schemalens serve --from snapshot.json` views it with no database.

**Packages.** Standard library only. The Mermaid test compares against a
golden file in `backend/pkg/render/testdata`.

**How it helps.** The snapshot also makes the UI testable without Postgres,
and it's the first step toward Phase 3, where we'll ship a production schema
somewhere else.

**Done when.** The Mermaid output renders on GitHub, and a snapshot loads in
`serve --from`.

---

## Step 11 — Watch the database for changes  `[x]`

**Why.** The schema isn't a document we generate once. It changes when
someone runs a migration. The picture must follow the database by itself,
without anyone pressing refresh.

**What we need.**
- We can't let Postgres tell us about changes. Event triggers +
  `LISTEN/NOTIFY` need `CREATE EVENT TRIGGER`, which is a write and needs
  superuser. So we ask.
- `backend/pkg/postgres/fingerprint.go`: one small query that returns an md5 of
  everything structural (tables, columns, types, nullability, defaults,
  constraints, indexes, comments). It runs in milliseconds.
- `backend/pkg/session/watcher.go`: every 2s (`--watch-interval`) compare the
  fingerprint. If it's the same, do nothing. If it changed, re-read the
  schema, rebuild relationships and findings, and swap the cached copy.
- Row counts, sizes and scan counts change all the time but don't change the
  structure. They're refreshed every 30s (`--stats-interval`), and we only
  announce it if findings or numbers actually moved.
- If the database goes away, keep showing the last good schema and retry
  with backoff.

**How it helps.** SchemaLens becomes something you leave open next to your
editor. Run a migration and watch the graph change. Phase 2 will reuse the
same loop to refresh query stats.

**Done when.** `ALTER TABLE ... ADD COLUMN` in psql shows up in the cached
schema within a few seconds, with no restart.

---

## Step 12 — HTTP server and live events  `[x]`

**Why.** The browser needs the schema and needs to hear when it changes.

**What we need.**
- `GET /` serves the embedded UI.
- `GET /api/schema` returns the current schema JSON.
- `GET /api/export/mermaid` returns the current Mermaid text.
- `GET /api/events` is a Server-Sent Events stream. It sends
  "schema changed, version N" whenever the watcher swaps the cache.
- Binds to `127.0.0.1` by default, because it shows schema details.

**Packages.**
- `net/http` with Go 1.22+ route patterns (`GET /api/schema`). No framework
  needed.
- `embed` to put the UI inside the binary. A tiny `frontend/embed.go` exports the
  files, because `go:embed` can't reach up into a parent folder.
- SSE instead of WebSockets: the data only flows one way (server → browser),
  it's plain HTTP, the browser reconnects by itself, and Go needs no extra
  library for it.

**How it helps.** The UI stays a thin viewer. All the thinking happens in Go
and is tested there.

**Done when.** `curl -N localhost:8080/api/events` prints an event as soon as
we alter a table.

---

## Step 13 — Draw the graph  `[x]`

**Why.** This is what people will actually look at.

**What we need.**
- Each table as an ER-style box: name on top, columns below with 🔑 PK, 🔗 FK,
  `U` unique, nullable dimmed. A "Names only / Show columns" toggle for big
  schemas.
- Edges from child to parent, labelled with the FK columns and `N:1` / `1:1`.
  Solid for real, dashed for guessed.
- Red border for a high finding, orange for medium.
- Click a table to highlight its neighbours. Double-click or Esc resets.
- Fit button, PNG export, dagre (default) or cose layout.
- Light and dark theme from `prefers-color-scheme`.
- Must stay usable at 200+ tables. "Names only" should lay out in under 2s.

**Packages.**
- **Cytoscape.js**: a mature graph library that handles pan, zoom,
  selection, styling and PNG export, and stays fast with hundreds of nodes.
- **cytoscape-dagre** (which bundles **dagre**): a layered left-to-right
  layout, which fits "child points to parent" naturally.
- Both are **copied into `frontend/vendor/`**, so the tool works offline and
  doesn't depend on a CDN.
- No React and no build step. Plain HTML, CSS and JS are enough for one page,
  and anyone can read them.

**How it helps.** It turns the model into something a developer understands
in seconds.

**Done when.** The sample database draws cleanly, and a generated 200-table
schema lays out in under 2 seconds.

---

## Step 14 — Sidebar and details panel  `[x]`

**Why.** The graph shows the shape. The panels answer "tell me more about
this table" and "what's wrong?".

**What we need.**
- **Left:** database name, version, a live status dot, table search (matches
  highlight, the rest fade), table list with rows and size, findings grouped
  by severity with a "Copy SQL" button.
- **Right**, when a table is selected: columns, indexes (size, scans, tagged
  unused or redundant), "references" and "referenced by" (click to jump), and
  this table's findings.

**How it helps.** Now a developer can go from "something is red" to "here's
the fix" without leaving the page.

**Done when.** Clicking a finding focuses its table, and "Copy SQL" copies
runnable SQL.

---

## Step 15 — Make the UI live  `[x]`

**Why.** Step 11 made the server notice changes. The browser must show them
without redrawing everything and losing the user's place.

**What we need.**
- `frontend/live.js` listens on `/api/events`, then fetches `/api/schema`.
- Compare old and new by table ID and relation ID:
  - new tables fade in near their neighbours
  - dropped tables fade out
  - changed tables flash briefly
  - existing tables **keep their positions**
- Keep the selection, zoom and search filter.
- Sidebar shows "● Live · last change 14:02:31", "Reconnecting…", or
  "Snapshot".

**Packages.** The browser's built-in `EventSource`. Nothing to install.

**How it helps.** This is what makes SchemaLens feel like a live view of the
database instead of a report.

**Done when.** Adding a table in psql makes it appear in an open browser tab
within a few seconds, and nothing else on the graph moves.

---

## Step 16 — Tests and CI  `[x]`

**Why.** Tests were written along the way (steps 7–10). Here we add the ones
that need a real database, and make sure every push is checked.

**What we need.**
- Integration test: load `sample_schema.sql`, run the reader, check the table
  count, FK count, partition folding, and that all seven finding kinds
  appear. It runs only when `SCHEMALENS_TEST_DSN` is set, so `go test ./...`
  still works on a laptop without Docker.
- `go vet` and `staticcheck` clean.
- A GitHub Actions workflow with a Postgres 16 service container.

**Packages.** Standard `testing`, `net/http/httptest`,
`honnef.co/go/tools/cmd/staticcheck`.

**How it helps.** Phase 2 will change a lot of this code. These tests tell us
right away if we broke Phase 1.

**Done when.** CI is green on the branch.

---

## Step 17 — README  `[x]`

**Why.** It's a portfolio project. The README is the first thing people see.

**What we need.** The pitch, a screenshot or GIF of the live graph, a
quickstart (`make demo`), CLI usage, the list of findings with why they
matter, an architecture diagram, and the roadmap below.

**Done when.** Someone new can go from `git clone` to seeing the graph in
under five minutes.

---

## Phase 1.1 — Connect from the browser, and keep the graphs

Phase 1 needed the connection string on the command line. That's fine for a
developer in a terminal, but it means restarting the tool to look at another
database, and every graph disappears when the tool stops. These steps fix
both.

---

## Step 18 — Connect to a database from the page  `[x]`

**Why.** You should be able to start `schemalens serve` with nothing, open the
page, paste a connection URL (or type host, user, password...) and see the
graph. Switching to another database should not need a restart.

**What we need.**
- `serve` without `--dsn` opens on a **Connect** screen instead of failing.
- The form takes either a full URL (`postgres://user:pw@host:5432/db`) or the
  separate fields: host, port, database, user, password, SSL mode, and
  optionally which schemas to read.
- `POST /api/connect` connects (read-only, as always), reads the schema, and
  only *then* swaps it in. A bad password leaves the current graph on screen
  and shows the error.
- The server can now switch sources while it runs: stop the old watcher,
  close the old pool, start watching the new one. The `Hub` gets a
  `Replace` that always counts as a new version, so every open tab switches
  over too.
- **The password lives in memory only.** It's never written to disk, never
  logged, and never sent back to the browser.
- The connect endpoint makes the tool open outbound connections, so another
  website must not be able to trigger it:
  - `POST`s must be JSON and same-origin (stops CSRF: a cross-site JSON
    POST needs a CORS preflight we never allow).
  - When bound to localhost, the `Host` header must be localhost (stops DNS
    rebinding).

**Packages.** Nothing new: `net/url` builds the URL from the fields
(escaping the password properly), and `pgconn.ParseConfig` (already part
of pgx) reads host, port, database and user back out of a pasted URL for
display.

**How it helps.** The tool becomes something you open once and point at
whatever database you're working on.

**Done when.** Starting with no flags, connecting through the form draws the
graph. A wrong password shows the error without losing the current graph.
Connecting to a second database replaces the first in every open tab.

---

## Step 19 — Save graphs  `[x]`

**Why.** A graph is worth keeping: to compare after a migration, to look at
without the database, or just because you spent five minutes arranging the
tables. A "saved graph" is the schema at that moment **plus the positions**
of the tables on screen, so it reopens exactly as you left it.

**What we need.**
- A small store: one JSON file per saved graph in a data directory
  (`~/.config/schemalens/graphs` by default, `--data-dir` to change it).
  - Files are written to a temp file then renamed, so a crash never leaves
    half a file.
  - Files are `0600`, because they describe your schema.
  - IDs are checked against a strict pattern, so a crafted ID can't reach
    outside the directory.
- What's saved: a name, when, the schema (tables, relations, findings), the
  table positions, the "names only / columns" mode, and where it came from
  (host, port, database, user, schemas). **Never the password.**
- `GET /api/graphs` lists them, `POST /api/graphs` saves the current one,
  `DELETE /api/graphs/{id}` removes one.

**Packages.** Standard library: `os`, `encoding/json`, `path/filepath`.

**How it helps.** Graphs outlive the process. The same files are also
exactly what Phase 2 needs to compare a schema before and after a change.

**Done when.** Unit tests cover save, list, open, delete, and rejecting bad
IDs. A saved graph survives a restart of `serve`.

---

## Step 20 — Open saved graphs in the UI  `[x]`

**Why.** Saving is only useful if getting back is one click.

**What we need.**
- A **Save** button in the toolbar that asks for a name.
- The Connect screen lists saved graphs: open, delete, or **Reconnect**
  (fills in the form from where it came from, minus the password).
- Opening one (`POST /api/graphs/{id}/open`) shows it as a snapshot with
  its saved positions; `/api/layout` hands the positions to the page.
- Whenever the source changes (another database, a saved graph), the page
  starts a fresh graph instead of patching the old one.

**Done when.** In a browser test: connect with the form, move a table,
save, connect to another database, open the saved graph, and the moved
table is where it was left.

---

## Step 21 — Colour tables by schema  `[x]`

**Why.** A database is often split into schemas: `public` for the app,
`billing` for payments, `cart` for shopping. On the graph they all look the
same, so you can't see at a glance which tables belong together, or when a
relation crosses from one schema into another.

**What we need.**
- Every schema other than `public` gets its own colour: the card's header
  gets a clear tint, the body a soft one. `public` keeps the plain look, so
  the other schemas stand out. In "names only" mode the whole box is tinted.
- Colours are handed out in alphabetical order of schema name from a fixed
  palette (red, blue, green, purple, teal, amber, pink, brown), so `billing`
  is always red in a database with only `billing` besides `public`. After
  eight schemas the palette repeats.
- Tints are mixed with the theme's card colour, so they work in both light
  and dark mode and the text stays readable.
- It must not be confused with findings: those stay as thick red/orange
  *borders*; the schema colour is a *background*.
- The same colour appears in the legend (one chip per schema), as a stripe
  in the sidebar's table list, and on the schema badge in the details panel.
- It follows live changes: create a new schema with a table and it gets a
  colour straight away.

**Packages.** None. A small `frontend/colors.js` shared by the graph and the
page.

**Done when.** In a browser test, `billing` tables are red-tinted, `public`
tables are plain, a schema created live gets the next colour, and the
legend lists them.

---

## After Phase 1 (not built now)

What we build here is shaped so these are easy later:

- **Phase 2: query analysis.** Read `pg_stat_statements`, run `EXPLAIN`, find
  seq scans on big tables, and colour hot tables on the graph. It reuses the
  row estimates and index stats from steps 2 and 5, and the live loop from
  step 11.
- **Phase 3: shift-left.** Find SQL in Go code, EXPLAIN it against a shadow
  database with the production schema and planner stats, and comment on the
  PR. It reuses the snapshot from step 10.
- **Later:** MySQL, as a second implementation of the `Introspector`
  interface from step 6.
