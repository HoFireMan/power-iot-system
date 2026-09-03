package migrations

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// latestEmbeddedMigrationVersion reports the newest embedded schema source.
// Generic runtime bootstrap still caps execution at protectedSchemaVersion;
// later migration bodies are private operator inputs and are never selected by
// the public runtime path.
func latestEmbeddedMigrationVersion() (int, error) {
	entries, err := fs.ReadDir(Files, "sql")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	latest := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("invalid embedded migration filename %q", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return 0, fmt.Errorf("invalid embedded migration version in %q", name)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("no embedded up migrations found")
	}
	return latest, nil
}
