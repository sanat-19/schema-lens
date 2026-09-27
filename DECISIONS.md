# Decisions

Things we decided while building SchemaLens, especially where we went a
different way from the original brief, and why. Newest at the bottom.

---

### Live updates: we poll a catalog fingerprint, we don't use event triggers

The UI has to follow the database without anyone pressing refresh. Postgres
can push DDL changes to us with an event trigger plus `LISTEN/NOTIFY`, but
creating an event trigger is a write and needs superuser. SchemaLens promises
to be read-only, so that's out.

Instead we ask, cheaply: one query hashes the structural parts of the catalog
for the selected schemas. If the hash hasn't changed, we do nothing. If it
has, we re-read the schema and tell the browser over Server-Sent Events.

### Column numbers are turned into names in SQL, not in Go

The brief suggested keeping a `(table oid, attnum) → name` map in Go to decode
`conkey`, `confkey` and `indkey`. We resolve them inside the catalog queries
instead (`unnest(... ) WITH ORDINALITY` joined to `pg_attribute`).

Why: an FK can point at a table in a schema we didn't load, and then the Go
map wouldn't have that table's columns. Doing it in SQL gives the right names
in every case, keeps the column order, and is less code.

### Expression index keys come from `pg_get_indexdef(index, position)`

Instead of parsing the full `CREATE INDEX` text to find the expression for an
`indkey` entry of 0, we ask Postgres for that one position's text. It
handles nested parentheses, casts and collations correctly, which a
hand-written parser wouldn't.

### Partitioned tables and indexes are summed over their leaf partitions

A partitioned parent stores nothing itself, so its own size is 0 and its
index scan count is empty. We use `pg_partition_tree()` to add up the leaf
partitions: rows, total size, index size and index scans. That also handles
sub-partitioning. If none of the leaves has been analyzed, the row estimate
stays -1 ("never analyzed").

### Unique indexes count as unique keys, not just unique constraints

`CREATE UNIQUE INDEX` guarantees uniqueness exactly like a `UNIQUE`
constraint, and plenty of schemas use the index form. So a unique index that
is not partial and has no expression keys is added to `Table.Uniques`. That
matters for one-to-one detection.

### The whole introspection runs in one REPEATABLE READ transaction

So every catalog query sees the same moment in time. Without it, a migration
landing between two queries could give us an FK pointing at a table we
didn't see.

### No bundled demo database

`make run` opens the Connect page instead of starting a Docker Postgres with
a demo schema: SchemaLens is for looking at your own databases, and Docker
shouldn't be a requirement. `testdata/sample_schema.sql` stays as the fixture
for the integration tests.

### One-to-one when the FK columns *contain* a unique key

The brief said one-to-one when the FK columns exactly equal the child's PK
or a unique key. We use "contain" instead: if the FK is `(a, b)` and `a` alone
is unique, no two child rows can point at the same parent either. Exact
matches are still one-to-one, so the brief's cases behave the same.

### Guessing relations: when we refuse to guess

- If the column's own schema has a matching table, we use it. If not, and
  exactly one other loaded schema has one, we use that. If two or more do, we
  can't tell which is meant, so we don't guess. A wrong dashed edge is worse
  than none.
- A table's own primary key named like a link (`users.user_id`) is not
  treated as a link to itself.
- camelCase needs a capital `I` (`productId`), so a word like `paid` is not
  read as `pa` + `id`.
- Types are compared by family: all integer sizes match each other, and text,
  varchar and char match each other. Anything else must be the same base type.

### Findings: where we went a bit further than the brief

- **`unused_index` skips indexes that serve a foreign key** (real or
  inferred). An FK index can show 0 scans for weeks if parents are rarely
  deleted, but dropping it brings back exactly the full-table scan that
  `missing_fk_index` warns about. Telling someone to drop it would be bad
  advice.
- **Each index is reported once.** If an index is a duplicate, we don't also
  call it redundant or unused. One clear "drop this" is enough.
- **Which duplicate to keep:** the primary key, then a unique index, then the
  most-scanned, then by name. We never suggest dropping a primary key.
- **`redundant_index` skips short indexes with `INCLUDE` columns.** They are
  usually there for index-only scans that the longer index can't do.
- **`fk_type_mismatch` ignores length modifiers.** `varchar(10)` → `varchar(3)`
  compares without a cast, so it isn't flagged. `integer` → `bigint` is, and
  the explanation names the real risk: the child column overflows once parent
  ids pass 2³¹.
- **Partitioned tables get different SQL.** `CREATE INDEX CONCURRENTLY` and
  `DROP INDEX CONCURRENTLY` fail on partitioned tables, so those suggestions
  say how to avoid a long lock instead.
- **`no_primary_key` promotes a unique NOT NULL key if there is one**, before
  suggesting a brand-new identity column.
- **Inference checks the declared FKs, not `Column.IsFK`.** A unit test
  caught this: a schema from another source (a snapshot, a future MySQL
  reader) may not set the flag.

Every suggestion is checked against the demo database by running it inside
a transaction that is rolled back.

### Mermaid: entity names and types are simplified

Mermaid only accepts letters, digits, `_` and `-` in entity names and
attribute types. So `billing.payments` becomes `billing_payments` (tables in
`public` keep their plain name), and types lose their modifier and spaces:
`character varying(255)` becomes `character_varying`. The diagram is for
reading relationships, and the JSON export keeps the exact types. The output
is checked with Mermaid 11's own parser.

### `snapshot` is `export --format json` to a file

They produce the same JSON, relations and findings included, so `serve
--from` doesn't need to recompute anything. `snapshot` exists as its own
command because "save this to look at later" is a different intent from
"print this", and it says what it saved.

### How the live loop behaves

- **Two clocks.** The structure fingerprint is checked every 2s
  (`--watch-interval`, measured at about 1.6 ms per check on the demo DB). A
  full re-read happens only when it changes, or every 30s
  (`--stats-interval`) to pick up new row counts, sizes and index scans.
- **Browsers are only told about real changes.** A re-read that gives the
  same schema (apart from the capture time) doesn't bump the version, so a
  quiet database doesn't make every open tab re-fetch every 30s.
- **Events say "something changed", not what.** The SSE message is just the
  new version number and time; the browser then fetches `/api/schema`. A tab
  that missed a few events simply gets the latest version.
- **If the database goes away**, the last good schema stays on screen, the
  status turns to "reconnecting", and checks back off from 2s up to 30s.
- **The first read must succeed.** If `serve` can't read the schema at
  start-up there is nothing to show, so it exits with code 2. Later failures
  only change the status.
- **Ctrl+C ends open event streams.** Every request's context comes from the
  signal context, so shutdown doesn't hang on connected browsers.

### UI: vendored files

`cytoscape-dagre` 4.x bundles the dagre layout engine, so `frontend/public/vendor/` holds
two scripts (`cytoscape.min.js`, `cytoscape-dagre.js`) instead of the three
the brief listed. Versions and licences are in `frontend/public/vendor/VERSIONS`.
They stay plain `<script>` tags, which Vite copies as they are.

### UI: each table is an SVG card

Cytoscape can't put HTML inside a node, and one text label can't mix a bold
header with dimmed nullable columns. So each table is drawn as a small SVG
(header, then one row per column with 🔑 / 🔗 / U / ? markers) and used as the
node's background. Cards with more than 30 columns show "… N more columns";
the details panel has the full list. The SVGs are redrawn when the OS theme
changes, since their colours are baked in.

Schemas with more than 60 tables open in "names only" mode. Measured with a
generated 250-table, 362-relation schema in headless Chromium: the layout
takes about 0.4s in names-only mode and 0.5s with columns.

### UI: live patching keeps the user's place

When a new schema arrives, the graph is patched instead of re-laid out:
existing tables keep their positions, new ones are placed where the layout
would have put them (a child to the left of the table it references, a parent
to the right, in the first free spot), dropped ones fade out, and changed
ones flash. Selection, search, zoom and the details panel stay as they were.
`/api/schema` sends an `X-Schema-Version` header so the page can tell whether
the version it shows is the latest one announced on `/api/events`.

Checked in headless Chromium by running `CREATE TABLE`, `ADD COLUMN`,
`CREATE INDEX` and `DROP TABLE` in psql with the page open: each appeared in
1–2 seconds, and no existing table moved.

### Connecting from the page (Phase 1.1)

The brief had the database fixed on the command line. Now `serve` can start
with nothing and connect from the page, which means the server switches
sources while running:

- **A connect only replaces the graph once the first read has worked.**
  `Watcher.Prime` reads without publishing; a wrong password or missing
  permission leaves the current graph on screen and shows the error.
- **Switching is strictly ordered:** stop the old watcher and wait for it to
  exit, mark it stopped (so a Refresh that is still running can't publish
  the old database), close its pool, and only then swap in the new schema.
  A test checks that changing the old database afterwards changes nothing.
- **Every switch is a new "source"** with its own key. The page starts a
  fresh graph for a new source instead of patching the old one, and ignores
  a `/api/schema` answer that belongs to a different source than it expects.
- **The password is in memory only.** It's part of the DSN the pool holds
  (it needs it to reconnect), but it's never logged, never saved, never
  sent back, and the page clears the field once it has been used. The URL
  field is a password field, because URLs usually contain one.
- **The form can build the URL.** `url.UserPassword` escapes the password,
  so `p@ss/word?` can't break the URL. A test checks the round trip.

### Protecting /api/connect from other websites

Any page you visit can send requests to `localhost:8080`, and this endpoint
makes the tool open outbound connections. So:

- POSTs must be `application/json`. Browsers can only send "simple" content
  types cross-site without a CORS preflight, and we never answer one.
- A foreign `Origin` header is refused.
- When listening on a loopback address, a `Host` header that isn't
  localhost is refused. That stops DNS rebinding, where evil.example points
  its own name at 127.0.0.1 to look same-origin.

### Saved graphs

The brief ruled out persistent storage for Phase 1; saving was asked for
afterwards, so it's the one piece of storage, and deliberately small:

- One JSON file per graph in the user's config directory (`--data-dir` to
  change it). No database, no index file to get out of sync: listing reads
  the directory and skips files it can't parse.
- A saved graph is the **schema plus the table positions** and the
  names/columns mode, so it reopens exactly as it was left. It also records
  host, port, database, user, SSL mode and schemas, for Reconnect; **never
  the password**. A test reads the saved file back and checks it isn't there.
- Files are `0600` and the directory `0700`, since they describe the schema.
  Writes go to a temp file that's renamed into place, so a crash can't leave
  half a file. IDs come back in URLs, so they're checked against
  `^[a-z0-9][a-z0-9-]*$` before touching the disk.
- Opening a saved graph shows it as a snapshot and disconnects the live
  database, because the page shows one source at a time.

Checked in headless Chromium: start with no flags, get a wrong-password
error, connect through the form, confirm it's live, drag a table, save,
switch to another database, open the saved graph, and the dragged table is
back where it was left.

### Colouring tables by schema

- **`public` stays plain; every other schema gets a colour.** Plain `public`
  is what makes the others stand out. In a database without `public`, every
  schema is coloured.
- **Colours go out in alphabetical order**, from a fixed palette of eight
  (red, blue, green, purple, teal, amber, pink, brown). The same database
  always gets the same colours, and the page and the graph work them out the
  same way (`frontend/colors.js`). The trade-off: a new schema that sorts earlier
  shifts the colours of the ones after it. We preferred predictable colours
  over hashing names, which would clash as soon as two schemas hashed to the
  same colour.
- **A background tint, not a border.** Thick red/orange borders already mean
  high/medium findings. The schema colour tints the header clearly and the
  body softly, mixed with the theme's card colour, so it works in light and
  dark mode and never hides a finding.

Checked in headless Chromium in both themes: `billing` is red-tinted,
`public` is plain, and a `cart` schema created live in psql turned blue
within seconds while `billing` stayed red.

### Frontend and backend run separately

The page used to be built into the Go binary. Now `frontend/` is a Vite
project and the backend only serves the API. The Vite server forwards
`/api` to the backend instead of the page calling the backend's port
directly, so:

- the page and the API share an origin, and the page's code still calls
  plain `/api/...`;
- the backend needs no CORS, and its cross-site guard (JSON only, same
  Origin, localhost Host) works unchanged. `changeOrigin` stays off in
  `vite.config.js` for exactly that reason.

The cost: two processes instead of one binary, and Node to run the page.
While the backend is down, Vite answers `/api/events` with an HTTP error,
and `EventSource` gives up for good on one of those, unlike a dropped
connection. `live.js` opens a new stream when that happens.


### The backend is only the API server

Once the page moved to Vite, the command line's `serve`, `snapshot` and
`export` commands had little left to do, so they were removed: `go run
./backend` starts the API and that's all. Connecting happens on the page,
and Mermaid export is the toolbar button (`/api/export/mermaid`). The
entries above about `snapshot`, `serve --from` and `--dsn` describe the
earlier command line.

What went with them: dumping a schema from the terminal, viewing a snapshot
file without a database (saved graphs cover most of that), and the
`--dsn`/`$DATABASE_URL`, `--schemas`, `--watch-interval` and
`--stats-interval` flags. The watch and stats intervals are fixed at 2s and
30s in `main.go`.
