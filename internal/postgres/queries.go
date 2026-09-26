package postgres

// The catalog queries, in the order Introspect runs them.
//
// All of them read pg_catalog rather than information_schema: it's faster,
// it has the planner's statistics, and information_schema quietly hides
// objects the current user lacks column privileges on.
//
// Every query after the first two takes the list of table oids as $1 and
// covers all tables in one round trip, instead of one query per table.

// Schemas the user didn't pick explicitly: everything that isn't Postgres' own.
const qDefaultSchemas = `
SELECT nspname
FROM pg_namespace
WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND nspname NOT LIKE 'pg_temp_%'
  AND nspname NOT LIKE 'pg_toast_temp_%'
ORDER BY nspname`

const qServerInfo = `SELECT current_database(), current_setting('server_version')`

// Ordinary and partitioned tables, but not the partitions themselves.
//
// A partitioned parent holds no data of its own, so its rows and size are
// the sums over its leaf partitions. reltuples is -1 on PG14+ for a table
// that has never been analyzed; we keep -1 when no leaf has been analyzed.
const qTables = `
SELECT c.oid,
       n.nspname,
       c.relname,
       c.relkind = 'p' AS partitioned,
       coalesce(obj_description(c.oid, 'pg_class'), ''),
       CASE WHEN c.relkind = 'r' THEN c.reltuples::bigint ELSE leaves.row_estimate END,
       CASE WHEN c.relkind = 'r' THEN pg_total_relation_size(c.oid) ELSE leaves.total_bytes END
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL (
    SELECT CASE WHEN count(*) FILTER (WHERE l.reltuples >= 0) = 0 AND count(*) > 0 THEN -1
                ELSE coalesce(sum(l.reltuples) FILTER (WHERE l.reltuples >= 0), 0)::bigint
           END AS row_estimate,
           coalesce(sum(pg_total_relation_size(l.oid)), 0)::bigint AS total_bytes
    FROM pg_partition_tree(c.oid) AS tree
    JOIN pg_class l ON l.oid = tree.relid
    WHERE tree.isleaf
) AS leaves
WHERE c.relkind IN ('r', 'p')
  AND NOT c.relispartition
  AND n.nspname = ANY($1)
ORDER BY n.nspname, c.relname`

// Live columns, in table order. attnum > 0 skips system columns like ctid;
// attisdropped skips columns that were dropped but still leave a row here.
const qColumns = `
SELECT a.attrelid,
       a.attname,
       format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       coalesce(pg_get_expr(d.adbin, d.adrelid), ''),
       a.attidentity::text,
       a.attgenerated::text,
       coalesce(col_description(a.attrelid, a.attnum), '')
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = ANY($1::oid[])
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attrelid, a.attnum`

// Primary keys, unique constraints and foreign keys.
//
// conparentid = 0 skips the copies Postgres makes of a constraint on every
// partition. Constraints store column *numbers*; we turn them into names
// here, keeping their order, because the referenced table may be in a
// schema we didn't load and so wouldn't have its columns in memory.
const qConstraints = `
SELECT con.conrelid,
       con.conname,
       con.contype::text,
       ARRAY(SELECT a.attname
             FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, pos)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
             ORDER BY k.pos) AS columns,
       coalesce(rn.nspname, ''),
       coalesce(rc.relname, ''),
       ARRAY(SELECT a.attname
             FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, pos)
             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
             ORDER BY k.pos) AS ref_columns,
       con.confdeltype::text,
       con.confupdtype::text
FROM pg_constraint con
LEFT JOIN pg_class rc ON rc.oid = con.confrelid
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.conrelid = ANY($1::oid[])
  AND con.contype IN ('p', 'u', 'f')
  AND con.conparentid = 0
ORDER BY con.conrelid, con.conname`

// Indexes.
//
// indkey lists the key columns first and INCLUDE columns after them;
// indnkeyatts says where the key part ends. A 0 in indkey means that key is
// an expression, and pg_get_indexdef(index, position) gives us its text.
//
// An index on a partitioned table is empty itself (relkind 'I'); its size
// and scan count are the sums over the partitions' indexes. Scans are -1
// when no statistics exist for the index at all.
const qIndexes = `
SELECT i.indrelid,
       ic.relname,
       am.amname,
       i.indisunique,
       i.indisprimary,
       i.indpred IS NOT NULL,
       coalesce(pg_get_expr(i.indpred, i.indrelid, true), ''),
       pg_get_indexdef(i.indexrelid),
       ARRAY(SELECT CASE WHEN i.indkey[k - 1] = 0
                         THEN pg_get_indexdef(i.indexrelid, k, true)
                         ELSE (SELECT attname FROM pg_attribute
                               WHERE attrelid = i.indrelid AND attnum = i.indkey[k - 1])
                    END
             FROM generate_series(1, i.indnkeyatts) AS k
             ORDER BY k) AS key_columns,
       ARRAY(SELECT a.attname
             FROM generate_series(i.indnkeyatts + 1, i.indnatts) AS k
             JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[k - 1]
             ORDER BY k) AS include_columns,
       usage.scans,
       usage.bytes
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_am am ON am.oid = ic.relam
CROSS JOIN LATERAL (
    SELECT coalesce(sum(s.idx_scan), -1)::bigint AS scans,
           coalesce(sum(pg_relation_size(parts.oid)), 0)::bigint AS bytes
    FROM (SELECT i.indexrelid AS oid WHERE ic.relkind = 'i'
          UNION ALL
          SELECT tree.relid FROM pg_partition_tree(i.indexrelid) AS tree
          WHERE ic.relkind = 'I' AND tree.isleaf) AS parts
    LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = parts.oid
) AS usage
WHERE i.indrelid = ANY($1::oid[])
ORDER BY i.indrelid, ic.relname`
