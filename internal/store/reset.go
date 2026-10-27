package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ResetData moves the live SQLite database aside so the next Open starts from
// a clean install (first-run setup, fresh master key).
//
// It never deletes data: the database is renamed to
// myrouter-<timestamp>.reset.bak inside backups/, so an accidental reset is
// still recoverable by hand. Missing files are not an error, which makes the
// call safe on a first run.
func ResetData(dataDir string) (moved []string, err error) {
	files := []string{"myrouter.db", "myrouter.db-shm", "myrouter.db-wal", "master.key"}
	backupDir := filepath.Join(dataDir, "backups")
	stamp := time.Now().Format("20060102-150405")

	for _, name := range files {
		src := filepath.Join(dataDir, name)
		if _, statErr := os.Stat(src); statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return moved, fmt.Errorf("inspect %s: %w", src, statErr)
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return moved, fmt.Errorf("create backup dir: %w", err)
		}
		dst := filepath.Join(backupDir, fmt.Sprintf("%s-%s.reset.bak", name, stamp))
		if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("move %s aside: %w", src, err)
		}
		moved = append(moved, dst)
	}
	return moved, nil
}
