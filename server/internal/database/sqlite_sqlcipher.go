//go:build sqlcipher

package database

import (
	"fmt"

	"gorm.io/gorm"
)

// init marks this build as having SQLCipher support.
func init() {
	sqlcipherBuild = true
}

// openSQLiteImpl opens a SQLite database with SQLCipher encryption.
//
// Production Recommendation: Use PostgreSQL (RRT_DB_DRIVER=postgres) for
// production deployments. PostgreSQL provides robust encryption at the
// connection layer (sslmode=require) and is the recommended production backend.
//
// SQLite + SQLCipher is intended for development/testing scenarios where
// at-rest encryption is required but a full PostgreSQL deployment is not
// warranted.
//
// To enable real SQLCipher encryption:
//
//  1. Install SQLCipher C library:
//     - Debian/Ubuntu: apt-get install libsqlcipher-dev
//     - macOS: brew install sqlcipher
//  2. Add SQLCipher-enabled SQLite driver to go.mod:
//     go get github.com/mutecomm/go-sqlcipher/v4
//  3. Register the driver and create a GORM Dialector that uses it.
//     The integration requires a custom GORM Dialector because
//     gorm.io/driver/sqlite hardcodes the "sqlite3" driver name.
//  4. Build with: CGO_ENABLED=1 go build -tags sqlcipher
//  5. Set RRT_SQLITE_KEY environment variable to the encryption key.
//
// Until the SQLCipher driver is fully integrated, this function returns an
// error guiding the user to PostgreSQL or to complete the integration steps.
func openSQLiteImpl(databaseURL, encryptionKey string) (gorm.Dialector, error) {
	if encryptionKey == "" {
		return nil, fmt.Errorf("SQLCipher build requires encryption key (set RRT_SQLITE_KEY)")
	}
	return nil, fmt.Errorf("sqlcipher build tag is set but SQLCipher driver is not fully integrated; " +
		"use PostgreSQL (RRT_DB_DRIVER=postgres) for production, or complete the integration steps " +
		"documented in sqlite_sqlcipher.go to enable SQLite encryption")
}
