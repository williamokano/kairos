package registry

import (
	"fmt"
	"os"
)

// Load reads, parses, defaults, and validates the workflow definition at
// path. It is the one entrypoint later layers (L04 CLI, L05 engine) call;
// they never call Parse/ApplyDefaults/Validate directly.
func Load(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("registry: reading %s: %w", path, err)
	}
	return LoadBytes(data, path)
}

// LoadBytes is Load without a filesystem read, for callers that already
// have the YAML in memory (tests, embedded definitions).
func LoadBytes(data []byte, sourcePath string) (Definition, error) {
	doc, err := Parse(data)
	if err != nil {
		return Definition{}, err
	}
	def, err := ApplyDefaults(doc)
	if err != nil {
		return Definition{}, fmt.Errorf("registry: defaulting %s: %w", sourcePath, err)
	}
	def.SourcePath = sourcePath
	if err := Validate(doc, def); err != nil {
		return Definition{}, fmt.Errorf("registry: validating %s: %w", sourcePath, err)
	}
	return def, nil
}
