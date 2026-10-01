package config

import (
	"os"
	"path/filepath"
)

// ServerDir returns the directory that should be used as the server root for
// resolving bundled resources (webhost, services/netease-api, etc.).
//
// Resolution order:
//   1. Directory containing the running binary (production).
//   2. When run via `go run`, os.Executable() points to a temporary build
//      directory, so we fall back to the current working directory.
//   3. We then walk up the directory tree looking for a directory that
//      contains the expected bundled resources (services/netease-api). This
//      allows the same binary or dev command to work from either the repo root
//      or the server/ subdirectory.
func ServerDir() string {
	candidates := []string{}

	ex, err := os.Executable()
	if err == nil {
		if exPath, err := filepath.EvalSymlinks(ex); err == nil {
			candidates = append(candidates, filepath.Dir(exPath))
		} else {
			candidates = append(candidates, filepath.Dir(ex))
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}

	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		// Walk up at most a few levels looking for the canonical markers.
		for d := dir; d != "" && d != filepath.Dir(d); d = filepath.Dir(d) {
			if isServerRoot(d) {
				return d
			}
		}
	}

	if len(candidates) > 0 && candidates[0] != "" {
		return candidates[0]
	}
	return "."
}

// isServerRoot reports whether dir looks like the server root by checking for
// the presence of bundled resources. The Netease API source is the canonical
// marker because it is part of the repository and not a runtime-generated path.
func isServerRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "services", "netease-api", "package.json"))
	return err == nil
}
