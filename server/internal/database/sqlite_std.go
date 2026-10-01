//go:build !sqlcipher

package database

import (
	"fmt"
	"os"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLiteImpl(databaseURL, encryptionKey string) (gorm.Dialector, error) {
	if encryptionKey != "" {
		// The default pure-Go SQLite driver does not implement SQLCipher.
		// Build with `-tags sqlcipher` and link against a SQLCipher-enabled
		// driver (e.g. github.com/mutecomm/go-sqlcipher/v4) to enable encryption.
		env := os.Getenv("RRT_ENV")
		if env == "production" {
			return nil, fmt.Errorf("RRT_SQLITE_KEY is set but binary was built without SQLCipher support; rebuild with -tags sqlcipher for production")
		}
		fmt.Fprintln(os.Stderr, "WARNING: SQLite encryption key is set but this binary was built without SQLCipher support.")
		fmt.Fprintln(os.Stderr, "         The database will be stored UNENCRYPTED. Rebuild with -tags sqlcipher to enable encryption.")
	}
	return sqlite.Open(databaseURL + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"), nil
}
