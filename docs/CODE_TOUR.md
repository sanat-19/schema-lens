# Code tour

A guided way into the SchemaLens code. Read it in the same order it was
built: each layer only uses the ones before it. [`DEVELOPMENT_PLAN.md`](../DEVELOPMENT_PLAN.md)
explains *why* each step exists; this file shows *where* it lives.

The whole project is about 6,500 lines, but about a dozen functions explain
how it works. Function names are given rather than line numbers, so search
for `func Name` (Go) or `function name` (JS).

---

## The big picture

Everything is one pipeline:

```
Postgres ──► read catalog ──► model ──► relations ──► findings ──► session ──► api ──► browser
          (backend/pkg/postgres/)  (backend/models/) (backend/pkg/graph/) (backend/pkg/analyze/) (backend/pkg/session/) (backend/api/)  (frontend/)
```

- **`models`** is the shared language: every struct lives here. It knows nothing about Postgres.
  Every other package fills it in or reads it.
- **`postgres`** is the only package that talks to the database.
- **`graph`** and **`analyze`** are pure functions: model in, answers out.
  No database, so they're easy to test.
- **`backend/pkg/session`** decides *what* is on screen (a live database or a
  saved graph) and keeps it live.
- **`api`** is only HTTP: it decodes the request, calls `pkg/`, and encodes
  the answer. No decisions are made there.
- **`frontend`** only draws what the backend gives it.

---

## Reading order

### 1. The model: `backend/models/`

[`schema.go`](../backend/models/schema.go): start with `Schema`, then `Table`,
`Column`, `ForeignKey`, `Index`, then `Relation` and `Finding`. Everything
else in the project is about filling in or reading these structs.

Notice `Table.ID()`: the `"schema.name"` string is the key for a table
everywhere, even in the browser.

### 2. Reading the database: `backend/pkg/postgres/`

- [`connect.go`](../backend/pkg/postgres/connect.go): how "read-only" is
  enforced (`default_transaction_read_only`, a statement timeout), and
  `Redact()`, which keeps passwords out of every message.
- [`introspect.go`](../backend/pkg/postgres/introspect.go), `Introspect()`: the
  step-by-step heart. It runs in one transaction: tables, then columns, then
  constraints, then indexes.
- [`queries.go`](../backend/pkg/postgres/queries.go): the SQL itself. Read
  `qTables` for how partitions are folded into their parent, and
  `qConstraints` for how column *numbers* are turned into names.
- [`fingerprint.go`](../backend/pkg/postgres/fingerprint.go): one small query
  that hashes the structure, so we can tell cheaply whether anything
  changed. You'll need it in step 6.

**Try it.** Seeing the real JSON makes the model concrete: connect from the
page, then open http://localhost:5173/api/schema.

### 3. Relationships: `backend/pkg/graph/`

- [`relations.go`](../backend/pkg/graph/relations.go), `Relations()`: one edge
  per foreign key. `cardinality()` is a ten-line answer to "is it 1:1 or N:1?".
- [`infer.go`](../backend/pkg/graph/infer.go), `inferRelations()`: guessing links
  that have no FK, from column names. `referencedThing()` turns `product_id`
  into `product`, and `tableNamesFor()` tries `products`, `categories`, ...
- Read [`relations_test.go`](../backend/pkg/graph/relations_test.go) alongside
  it: each test is a tiny hand-made schema and what should come out.

### 4. Findings: `backend/pkg/analyze/`

- [`findings.go`](../backend/pkg/analyze/findings.go), `Findings()`: runs every
  check and sorts the results by severity.
- [`indexes.go`](../backend/pkg/analyze/indexes.go), `missingFKIndexes()`: the
  most important check. Once you get it, the others follow the same shape:
  loop over tables, check one rule, return a `Finding` with copy-able SQL.
- [`sql.go`](../backend/pkg/analyze/sql.go): quoting names so the suggested SQL
  runs even for a table called `order`.
- [`findings_test.go`](../backend/pkg/analyze/findings_test.go): every rule has
  a "should fire" test and a "should not fire" test.

### 5. Starting up: `backend/main.go` and `backend/pkg/connect/`

- [`main.go`](../backend/main.go): reads two flags, creates the session,
  loads the router and runs `ListenAndServe`.
- [`connect.go`](../backend/pkg/connect/connect.go), `Postgres()`: how a
  connect request from the page becomes a database connection. Its `Load`
  runs steps 3 and 4 on every freshly read schema.

### 6. Staying live: `backend/pkg/session/`, then `backend/api/`

Read in this order:

1. [`watcher.go`](../backend/pkg/session/watcher.go), `check()`: every 2 seconds
   it takes the cheap fingerprint, and re-reads the whole schema only when
   it changes. `reload()` does the re-read.
2. [`hub.go`](../backend/pkg/session/hub.go), `Publish()` vs `Replace()`:
   "the same database changed" vs "we're now looking at something else".
3. [`session.go`](../backend/pkg/session/session.go), `Connect()` and
   `stopLocked()`: switching databases safely. This is the trickiest code
   in the project, and the comments explain why the steps come in that order.
4. [`api/events.go`](../backend/api/events.go): the Server-Sent Events
   stream that tells the browser something changed.
5. [`api/graphs.go`](../backend/api/graphs.go), the `Save…` methods in `session.go`,
   and [`store.go`](../backend/pkg/store/store.go): saved graphs, from request to disk.
6. [`router/router.go`](../backend/router/router.go): which URL goes to which
   handler, and [`router/guard.go`](../backend/router/guard.go): why other websites can't use
   the API through your browser.

### 7. The browser: `frontend/`

- [`app.js`](../frontend/app.js), `handleStatus()`: every message from the server
  goes through it. Follow it into `sourceChanged()`, `catchUp()` and
  `applySchema()`.
- [`graph.js`](../frontend/graph.js), `update()`: patching the graph without
  moving tables that are already there. `placeNear()` decides where a new
  table goes, and `tableCard()` draws one table as a small SVG.
- [`colors.js`](../frontend/colors.js): schema colours. The newest and smallest
  file, and a good warm-up.
- [`live.js`](../frontend/live.js): the `EventSource` connection, and why it
  reopens itself.
- [`vite.config.js`](../frontend/vite.config.js): the dev server, and how it
  forwards `/api` to the backend.

---

## Follow one change from end to end

Once you've skimmed the above, follow one real event through all the layers.
This is what ties everything together:

1. You run `ALTER TABLE products ADD COLUMN weight int` in psql.
2. Within 2 seconds `Watcher.check()` calls `Fingerprint()`, and the hash
   is different.
3. `reload()` → `Introspect()` → `enrich()` (relations, then findings).
4. `Hub.Publish()` sees a real difference, bumps the version, and broadcasts
   a `schema` event.
5. `events.go` writes `event: schema` to every open browser connection.
6. In the browser: `live.js` → `handleStatus()` → `catchUp()` →
   `fetch('/api/schema')`.
7. `applySchema()` → `graph.update()`: the `products` node's signature
   changed, so its card is redrawn and flashes. Nothing else moves.

Watch it happen: run `make run`, connect to a database, open the browser's developer tools on the
Network tab, click the `/api/events` request, and run that `ALTER TABLE`.

---

## Learn it by changing it

Small changes that each touch one layer, roughly easiest first:

1. **`web`:** show the number of findings in each table card's header.
   Only `tableCard()` in `graph.js`.
2. **`graph`:** make inference also understand `productID` (capital D).
   One place in `referencedThing()`, plus a test case.
3. **`analyze`:** add a low-severity rule for `timestamp without time zone`
   columns. Copy the shape of `noPrimaryKey()` and add a test.
4. **`postgres` → `schema` → `web`:** read `pg_stats.null_frac` for each
   column, add it to `Column`, and show it in the details panel. This one
   touches three layers, a good step up.

After each change:

```sh
go test ./...          # everything that doesn't need a database
make test-integration  # plus the tests against testdata/sample_schema.sql
make run               # and see it in the browser
```

---

## Where the "why" lives

- [`DEVELOPMENT_PLAN.md`](../DEVELOPMENT_PLAN.md): each step, what it needed,
  and how it helps the next one.
- [`DECISIONS.md`](../DECISIONS.md): every place we chose something
  non-obvious, and why.
- Code comments explain *why*, not *what*. If a line is surprising, the
  comment above it usually says what goes wrong without it.
