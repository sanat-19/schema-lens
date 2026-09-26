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

### Demo database runs on port 5433

`docker-compose.yml` maps Postgres to 5433 so it doesn't clash with a
Postgres many developers already have on 5432.

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
