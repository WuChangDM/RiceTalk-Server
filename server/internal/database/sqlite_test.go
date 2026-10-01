package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSQLite(t *testing.T) {
	t.Run("opens sqlite database without encryption key", func(t *testing.T) {
		dir := t.TempDir()
		dsn := filepath.Join(dir, "test.db")
		dialector, err := openSQLite(dsn, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dialector == nil {
			t.Fatalf("expected dialector")
		}
	})

	t.Run("warns but opens when encryption key is set without sqlcipher build", func(t *testing.T) {
		if isSQLCipherBuild() {
			t.Skip("skipping: built with sqlcipher tag")
		}
		dir := t.TempDir()
		dsn := filepath.Join(dir, "test.db")
		// Ensure parent dir exists.
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		dialector, err := openSQLite(dsn, "secret-key")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dialector == nil {
			t.Fatalf("expected dialector")
		}
	})
}

// isSQLCipherBuild reports whether the sqlcipher build tag is active.
func isSQLCipherBuild() bool {
	// sqlite_sqlcipher.go sets a package-level variable when the tag is active.
	return sqlcipherBuild
}
