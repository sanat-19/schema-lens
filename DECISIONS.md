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
