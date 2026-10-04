package server

import (
	"path/filepath"

	"ridgericetalk/internal/config"
)

// serverDir returns the directory that should be used for resolving bundled
// server resources (webhost, services, etc.). See config.ServerDir for the
// implementation which handles both compiled binaries and `go run`.
func serverDir() string {
	return config.ServerDir()
}

// webhostDir returns the absolute path to a named frontend distribution
// directory under the server's webhost/dist folder.
func webhostDir(name string) string {
	return filepath.Join(serverDir(), "server", "webhost", "dist", name)
}
