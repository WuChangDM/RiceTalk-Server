package database

import "gorm.io/gorm"

// sqlcipherBuild is set to true by the sqlcipher build file.
var sqlcipherBuild bool

// openSQLite opens a SQLite database. The concrete implementation is selected
// by build tags so that a SQLCipher-enabled driver can be substituted for
// production deployments requiring encryption at rest.
func openSQLite(databaseURL, encryptionKey string) (gorm.Dialector, error) {
	return openSQLiteImpl(databaseURL, encryptionKey)
}
