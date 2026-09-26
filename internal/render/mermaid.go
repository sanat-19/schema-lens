// Package render turns a schema into formats people paste elsewhere.
package render

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/sanat-19/schema-lens/internal/graph"
	"github.com/sanat-19/schema-lens/internal/schema"
)

// Mermaid writes the schema as a Mermaid erDiagram, which GitHub, GitLab and
// most docs tools render straight from a Markdown code block.
//
//	orders }o--|| users : "user_id"
//
// reads as "many orders (maybe none) belong to exactly one user". Guessed
// relations use a dotted line.
func Mermaid(w io.Writer, s *schema.Schema) error {
	var b strings.Builder
	b.WriteString("erDiagram\n")

	for _, t := range s.Tables {
		fmt.Fprintf(&b, "    %s {\n", entityName(t.Schema, t.Name))
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "        %s %s", mermaidWord(graph.BaseType(c.Type)), mermaidWord(c.Name))
			if keys := columnKeys(c); keys != "" {
				b.WriteString(" " + keys)
			}
			if c.Comment != "" {
				fmt.Fprintf(&b, " %q", strings.ReplaceAll(c.Comment, `"`, "'"))
			}
			b.WriteString("\n")
		}
		b.WriteString("    }\n")
	}

	for _, r := range s.Relations {
		child, parent := splitID(r.From), splitID(r.To)
		label := strings.Join(r.FromCols, ", ")
		if r.Inferred {
			label += " (inferred)"
		}
		fmt.Fprintf(&b, "    %s %s%s%s %s : %q\n",
			entityName(child[0], child[1]), childEnd(r), line(r), parentEnd(r),
			entityName(parent[0], parent[1]), label)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// childEnd is how many children one parent can have: many, or at most one.
func childEnd(r schema.Relation) string {
	if r.Cardinality == schema.OneToOne {
		return "|o"
	}
	return "}o"
}

// parentEnd is how many parents a child has: exactly one, or none if the
// FK column can be NULL.
func parentEnd(r schema.Relation) string {
	if r.Optional {
		return "o|"
	}
	return "||"
}

// line is solid for a declared FK and dotted for a guessed one.
func line(r schema.Relation) string {
	if r.Inferred {
		return ".."
	}
	return "--"
}

func columnKeys(c *schema.Column) string {
	var keys []string
	if c.IsPK {
		keys = append(keys, "PK")
	}
	if c.IsFK {
		keys = append(keys, "FK")
	}
	if c.IsUnique && !c.IsPK {
		keys = append(keys, "UK")
	}
	return strings.Join(keys, ", ")
}

// entityName gives each table a Mermaid-safe name. Tables in public keep
// their plain name; others get their schema in front (billing_payments).
func entityName(schemaName, table string) string {
	if schemaName == "public" {
		return mermaidWord(table)
	}
	return mermaidWord(schemaName + "_" + table)
}

var notMermaidWord = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// mermaidWord makes a name or type safe to use unquoted: Mermaid only
// allows letters, digits, _ and - there, so "timestamp with time zone"
// becomes "timestamp_with_time_zone".
func mermaidWord(s string) string {
	return strings.Trim(notMermaidWord.ReplaceAllString(s, "_"), "_")
}

func splitID(id string) [2]string {
	schemaName, name, _ := strings.Cut(id, ".")
	return [2]string{schemaName, name}
}
