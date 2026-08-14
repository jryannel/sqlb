// Package studio is a generic, uncurated browser over a sqlb schema: it reads
// a sqlb.json manifest and (in later stages) calls the generated REST API for
// data and actions. It carries no per-application knowledge — no row label,
// no field order, no widget hints — because a raw grid over declared columns
// needs none of that. See docs/adr/0053 in the parent module for the decision
// this package exists to test.
package studio

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jryannel/sqlb/schema"
)

// LoadManifest reads a schema.Manifest from a sqlb.json file at path.
func LoadManifest(path string) (*schema.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("studio: reading manifest: %w", err)
	}
	var m schema.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("studio: parsing manifest %s: %w", path, err)
	}
	return &m, nil
}
