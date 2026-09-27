package postgres

import (
	"context"
	"fmt"
)

// qFingerprint hashes everything that defines the *structure* of the loaded
// schemas: which tables exist, their columns, types, nullability, defaults,
// constraints, indexes and comments. If any of those change, so does the hash.
//
// It deliberately leaves out statistics (row counts, sizes, index scans),
// which move all the time without anyone changing the schema. Those are
// refreshed on a slower timer instead.
//
// It reads only small catalog tables, so it takes a few milliseconds even
// with hundreds of tables and is safe to run every couple of seconds.
//
// $1 is the list of schemas, or NULL for "every non-system schema", in which
// case creating a new schema changes the hash too.
const qFingerprint = `
WITH ns AS (
    SELECT oid, nspname
    FROM pg_namespace
    WHERE CASE WHEN $1::text[] IS NULL
               THEN nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
                    AND nspname NOT LIKE 'pg_temp_%'
                    AND nspname NOT LIKE 'pg_toast_temp_%'
               ELSE nspname = ANY($1::text[])
          END
),
tables AS (
    SELECT c.oid, ns.nspname, c.relname, c.relkind
    FROM pg_class c
    JOIN ns ON ns.oid = c.relnamespace
    WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
)
SELECT md5(concat_ws('|',
    (SELECT string_agg(nspname, ',' ORDER BY nspname) FROM ns),
    (SELECT string_agg(concat_ws(',', oid, nspname, relname, relkind), ';' ORDER BY oid) FROM tables),
    (SELECT string_agg(concat_ws(',', a.attrelid, a.attnum, a.attname, a.atttypid, a.atttypmod,
                                 a.attnotnull, a.attidentity, a.attgenerated, d.adbin),
                       ';' ORDER BY a.attrelid, a.attnum)
     FROM pg_attribute a
     LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
     WHERE a.attrelid IN (SELECT oid FROM tables) AND a.attnum > 0 AND NOT a.attisdropped),
    (SELECT string_agg(concat_ws(',', conrelid, conname, contype, conkey, confrelid, confkey,
                                 confdeltype, confupdtype),
                       ';' ORDER BY conrelid, conname)
     FROM pg_constraint
     WHERE conrelid IN (SELECT oid FROM tables) AND contype IN ('p', 'u', 'f') AND conparentid = 0),
    (SELECT string_agg(concat_ws(',', i.indrelid, i.indexrelid, ic.relname, ic.relam, i.indkey,
                                 i.indnkeyatts, i.indisunique, i.indisprimary, i.indexprs, i.indpred),
                       ';' ORDER BY i.indrelid, i.indexrelid)
     FROM pg_index i
     JOIN pg_class ic ON ic.oid = i.indexrelid
     WHERE i.indrelid IN (SELECT oid FROM tables)),
    (SELECT string_agg(concat_ws(',', objoid, objsubid, description), ';' ORDER BY objoid, objsubid)
     FROM pg_description
     WHERE classoid = 'pg_class'::regclass AND objoid IN (SELECT oid FROM tables))
))`

// Fingerprint returns a hash of the schema's structure. Two calls return the
// same value exactly when nothing structural changed in between.
func (in *Introspector) Fingerprint(ctx context.Context) (string, error) {
	var schemas []string // nil becomes SQL NULL: "all non-system schemas"
	if len(in.schemas) > 0 {
		schemas = in.schemas
	}
	var hash string
	if err := in.pool.QueryRow(ctx, qFingerprint, schemas).Scan(&hash); err != nil {
		return "", fmt.Errorf("fingerprinting schema: %w", err)
	}
	return hash, nil
}
