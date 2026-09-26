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
Postgres ──► read catalog ──► model ──► relations ──► findings ──► server ──► browser
            (postgres/)     (schema/)   (graph/)     (analyze/)   (server/)   (web/)
```

- **`schema`** is the shared language. It knows nothing about Postgres.
  Every other package fills it in or reads it.
- **`postgres`** is the only package that talks to the database.
- **`graph`** and **`analyze`** are pure functions: model in, answers out.
  No database, so they're easy to test.
- **`server`** decides *what* is on screen (a live database, a saved graph,
  a snapshot file) and keeps it live.
- **`web`** only draws what the server gives it.

---

## Reading order

### 1. The model: `internal/schema/`

[`model.go`](../internal/schema/model.go): start with `Schema`, then `Table`,
`Column`, `ForeignKey`, `Index`, then `Relation` and `Finding`. Everything
else in the project is about filling in or reading these structs.

Notice `Table.ID()`: the `"schema.name"` string is the key for a table
everywhere, even in the browser.

### 2. Reading the database: `internal/postgres/`

- [`connect.go`](../internal/postgres/connect.go): how "read-only" is
  enforced (`default_transaction_read_only`, a statement timeout), and
  `Redact()`, which keeps passwords out of every message.
- [`introspect.go`](../internal/postgres/introspect.go), `Introspect()`: the
  step-by-step heart. It runs in one transaction: tables, then columns, then
  constraints, then indexes.
- [`queries.go`](../internal/postgres/queries.go): the SQL itself. Read
  `qTables` for how partitions are folded into their parent, and
  `qConstraints` for how column *numbers* are turned into names.
- [`fingerprint.go`](../internal/postgres/fingerprint.go): one small query
  that hashes the structure, so we can tell cheaply whether anything
  changed. You'll need it in step 6.

**Try it.** Seeing the real JSON makes the model concrete:

```sh
make db-up
go run ./cmd/schemalens export \
  --dsn "postgres://schemalens:schemalens@localhost:5433/shop?sslmode=disable" | less
```

### 3. Relationships: `internal/graph/`

- [`relations.go`](../internal/graph/relations.go), `Relations()`: one edge
  per foreign key. `cardinality()` is a ten-line answer to "is it 1:1 or N:1?".
- [`infer.go`](../internal/graph/infer.go), `inferRelations()`: guessing links
  that have no FK, from column names. `referencedThing()` turns `product_id`
  into `product`, and `tableNamesFor()` tries `products`, `categories`, ...
- Read [`relations_test.go`](../internal/graph/relations_test.go) alongside
  it: each test is a tiny hand-made schema and what should come out.

### 4. Findings: `internal/analyze/`

- [`findings.go`](../internal/analyze/findings.go), `Findings()`: runs every
  check and sorts the results by severity.
- [`indexes.go`](../internal/analyze/indexes.go), `missingFKIndexes()`: the
  most important check. Once you get it, the others follow the same shape:
  loop over tables, check one rule, return a `Finding` with copy-able SQL.
- [`sql.go`](../internal/analyze/sql.go): quoting names so the suggested SQL
  runs even for a table called `order`.
- [`findings_test.go`](../internal/analyze/findings_test.go): every rule has
  a "should fire" test and a "should not fire" test.

### 5. The command line: `cmd/schemalens/`

- [`main.go`](../cmd/schemalens/main.go): subcommands and exit codes.
- [`commands.go`](../cmd/schemalens/commands.go), `enrich()`: two lines that
  run steps 3 and 4 on a freshly read schema.
- [`serve.go`](../cmd/schemalens/serve.go): `runServe()` wires the server
  together. `openPostgres()` is how a connect request from the page becomes a
  database connection.

### 6. Staying live: `internal/server/`

Read in this order:

1. [`watcher.go`](../internal/server/watcher.go), `check()`: every 2 seconds
   it takes the cheap fingerprint, and re-reads the whole schema only when
   it changes. `reload()` does the re-read.
2. [`hub.go`](../internal/server/hub.go), `Publish()` vs `Replace()`:
   "the same database changed" vs "we're now looking at something else".
3. [`server.go`](../internal/server/server.go), `Connect()` and
   `stopLocked()`: switching databases safely. This is the trickiest code
   in the project, and the comments explain why the steps come in that order.
4. [`events.go`](../internal/server/events.go): the Server-Sent Events
   stream that tells the browser something changed.
5. [`graphs.go`](../internal/server/graphs.go) and
   [`../internal/store/store.go`](../internal/store/store.go): saved graphs.
6. [`guard.go`](../internal/server/guard.go): why other websites can't use
   the API through your browser.

### 7. The browser: `web/`

- [`app.js`](../web/app.js), `handleStatus()`: every message from the server
  goes through it. Follow it into `sourceChanged()`, `catchUp()` and
  `applySchema()`.
- [`graph.js`](../web/graph.js), `update()`: patching the graph without
  moving tables that are already there. `placeNear()` decides where a new
  table goes, and `tableCard()` draws one table as a small SVG.
- [`colors.js`](../web/colors.js): schema colours. The newest and smallest
  file, and a good warm-up.
- [`live.js`](../web/live.js): the `EventSource` connection, ten lines.

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

Watch it happen: run `make demo`, open the browser's developer tools on the
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
make test-integration  # plus the tests against the demo database
make demo              # and see it in the browser
```

---

## Where the "why" lives

- [`DEVELOPMENT_PLAN.md`](../DEVELOPMENT_PLAN.md): each step, what it needed,
  and how it helps the next one.
- [`DECISIONS.md`](../DECISIONS.md): every place we chose something
  non-obvious, and why.
- Code comments explain *why*, not *what*. If a line is surprising, the
  comment above it usually says what goes wrong without it.
