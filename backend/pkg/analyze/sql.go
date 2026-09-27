package analyze

import (
	"fmt"
	"regexp"
	"strings"
)

// Helpers for writing suggestions as SQL that pastes and runs as-is, even
// when a table is called "order" or a column is called "userId".

var (
	plainIdent    = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	notIdentChars = regexp.MustCompile(`[^a-z0-9_]+`)
)

// Words Postgres won't accept as a bare table or column name. Not the full
// list, just the ones people actually name things after.
var reserved = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "both": true, "case": true,
	"cast": true, "check": true, "collate": true, "column": true,
	"constraint": true, "create": true, "current_date": true,
	"current_role": true, "current_time": true, "current_user": true,
	"default": true, "desc": true, "distinct": true, "do": true, "else": true,
	"end": true, "except": true, "false": true, "fetch": true, "for": true,
	"foreign": true, "from": true, "grant": true, "group": true,
	"having": true, "in": true, "limit": true, "not": true, "null": true,
	"offset": true, "on": true, "only": true, "or": true, "order": true,
	"primary": true, "references": true, "select": true, "table": true,
	"then": true, "to": true, "true": true, "union": true, "unique": true,
	"user": true, "using": true, "when": true, "where": true, "window": true,
	"with": true,
}

// ident quotes a name only when Postgres needs it quoted.
func ident(name string) string {
	if plainIdent.MatchString(name) && !reserved[name] {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// qualified gives schema.name, quoted as needed.
func qualified(schemaName, name string) string {
	return ident(schemaName) + "." + ident(name)
}

// columnList gives "a, b, c", quoted as needed.
func columnList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = ident(c)
	}
	return strings.Join(quoted, ", ")
}

// objectName builds a new index or constraint name like idx_orders_user_id,
// trimmed to Postgres' 63-byte limit.
func objectName(parts ...string) string {
	name := strings.ToLower(strings.Join(parts, "_"))
	name = notIdentChars.ReplaceAllString(name, "_")
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// humanBytes prints 1536 as "1.5 kB", the way pg_size_pretty would.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// humanCount prints 150000 as "150,000".
func humanCount(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
