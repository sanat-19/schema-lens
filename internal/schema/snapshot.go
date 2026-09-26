package schema

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// WriteJSON writes the schema as indented JSON. This is both the export
// format and the snapshot format.
func WriteJSON(w io.Writer, s *Schema) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// LoadSnapshot reads a schema saved earlier with WriteJSON.
func LoadSnapshot(path string) (*Schema, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var s Schema
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("reading snapshot %s: %w", path, err)
	}
	if len(s.Tables) == 0 {
		return nil, fmt.Errorf("snapshot %s has no tables; is it a SchemaLens snapshot?", path)
	}
	return &s, nil
}
