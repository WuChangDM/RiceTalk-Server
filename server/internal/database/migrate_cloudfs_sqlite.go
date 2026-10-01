package database

import (
	"fmt"

	"gorm.io/gorm"
)

// migrateCloudFSPartialUniqueIndexesSQLite rebuilds the CloudFS unique indexes
// as PARTIAL unique indexes (WHERE deleted_at IS NULL) on SQLite.
//
// Why this exists (DES-2026-0912-02 §2.5 / §6): versioned SQL migrations are
// PostgreSQL-only — SQLite (development/test) relies on GORM AutoMigrate. But
// AutoMigrate only creates an index when its name is missing; it never rewrites
// a pre-existing index to match a changed model tag. An existing dev database
// therefore keeps the old FULL unique indexes from 000001_baseline, and
// soft-deleted rows keep blocking re-use of a name.
//
// Fresh databases are already correct (AutoMigrate creates the indexes from the
// model tags, where the driver honours `where:deleted_at IS NULL`), so on a new
// database this is effectively a no-op; it is idempotent and cheap either way.
//
// Mirror of migrateScreenShareSQLiteCompat: SQLite-only, guarded, non-fatal.
func migrateCloudFSPartialUniqueIndexesSQLite(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}

	indexes := []struct {
		name  string
		table string
		stmt  string
	}{
		{
			name:  "idx_file_folder_name",
			table: "shared_file_entries",
			stmt: "CREATE UNIQUE INDEX IF NOT EXISTS idx_file_folder_name " +
				"ON shared_file_entries (folder_id, file_name) WHERE deleted_at IS NULL",
		},
		{
			name:  "idx_folder_parent_name",
			table: "shared_folders",
			stmt: "CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_parent_name " +
				"ON shared_folders (parent_id, name) WHERE deleted_at IS NULL",
		},
	}

	for _, idx := range indexes {
		var tableExists int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", idx.table).
			Scan(&tableExists).Error; err != nil {
			return fmt.Errorf("check %s existence: %w", idx.table, err)
		}
		if tableExists == 0 {
			// Table does not exist yet; AutoMigrate will create it together with
			// the partial index derived from the model tags.
			continue
		}

		// Drop the previous definition (full unique index) and recreate it as a
		// partial one. These composite indexes are always created via
		// CREATE INDEX (never a table-level UNIQUE constraint), so DROP works.
		if err := db.Exec("DROP INDEX IF EXISTS " + idx.name).Error; err != nil {
			return fmt.Errorf("drop %s: %w", idx.name, err)
		}
		if err := db.Exec(idx.stmt).Error; err != nil {
			return fmt.Errorf("recreate %s as partial unique index: %w", idx.name, err)
		}
	}

	return nil
}
