package config

import (
	"os"
	"path/filepath"
	"testing"
)

// layoutRepo builds the repository / dev layout under root:
//
//	root/services/netease-api/package.json
//	root/server/{cmd,core,internal}/
//	root/server/webhost/dist/admin/index.html
func layoutRepo(t *testing.T, root string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "services", "netease-api"))
	mustWriteFile(t, filepath.Join(root, "services", "netease-api", "package.json"), "{}\n")
	for _, sub := range []string{"cmd", "core", "internal"} {
		mustMkdirAll(t, filepath.Join(root, "server", sub))
	}
	mustMkdirAll(t, filepath.Join(root, "server", "webhost", "dist", "admin"))
	mustWriteFile(t, filepath.Join(root, "server", "webhost", "dist", "admin", "index.html"), "<html></html>\n")
}

// layoutOffline builds the offline bundle layout produced by install-offline.sh
// under root: server/ (module root incl. webhost) + migrations/ + livekit/ and
// deliberately NO services/ directory.
func layoutOffline(t *testing.T, root string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "server", "webhost", "dist", "admin"))
	mustWriteFile(t, filepath.Join(root, "server", "webhost", "dist", "admin", "index.html"), "<html></html>\n")
	mustMkdirAll(t, filepath.Join(root, "migrations"))
	mustWriteFile(t, filepath.Join(root, "migrations", "000001_baseline.up.sql"), "-- up\n")
	mustMkdirAll(t, filepath.Join(root, "livekit", "bin", "linux-amd64"))
}

// TestIsServerRootRepoLayout covers marker 1 (services/netease-api/package.json).
func TestIsServerRootRepoLayout(t *testing.T) {
	root := t.TempDir()
	layoutRepo(t, root)

	if !isServerRoot(root) {
		t.Fatalf("isServerRoot(%q) = false, want true (repo layout)", root)
	}
	// The module directory itself must not be mistaken for the root.
	if isServerRoot(filepath.Join(root, "server")) {
		t.Fatalf("isServerRoot(%q) = true, want false (module dir is not the root)", filepath.Join(root, "server"))
	}
}

// TestIsServerRootOfflineLayout covers marker 2: <d>/server/webhost/dist exists
// and <d>/services does not (offline bundle installed by install-offline.sh).
func TestIsServerRootOfflineLayout(t *testing.T) {
	root := t.TempDir()
	layoutOffline(t, root)

	if _, err := os.Stat(filepath.Join(root, "services")); err == nil {
		t.Fatalf("test fixture unexpectedly has services/: %q", root)
	}
	if !isServerRoot(root) {
		t.Fatalf("isServerRoot(%q) = false, want true (offline layout)", root)
	}
	// <root>/server has no nested <root>/server/server/webhost/dist marker.
	if isServerRoot(filepath.Join(root, "server")) {
		t.Fatalf("isServerRoot(%q) = true, want false (module dir is not the root)", filepath.Join(root, "server"))
	}
}

// TestIsServerRootRejectsWebhostDistFile guards the IsDir() requirement: a plain
// file named server/webhost/dist must not be accepted as a root marker.
func TestIsServerRootRejectsWebhostDistFile(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "server", "webhost"))
	mustWriteFile(t, filepath.Join(root, "server", "webhost", "dist"), "not a directory\n")

	if isServerRoot(root) {
		t.Fatalf("isServerRoot(%q) = true, want false (webhost/dist is a regular file)", root)
	}
}

// TestServerDirRepoLayout mirrors a binary/dev invocation started from
// <repo>/server: the walk-up must resolve to <repo> (not <repo>/server).
func TestServerDirRepoLayout(t *testing.T) {
	root := t.TempDir()
	layoutRepo(t, root)
	chdir(t, filepath.Join(root, "server"))

	if got := ServerDir(); got != root {
		t.Fatalf("ServerDir() = %q, want %q (repo layout)", got, root)
	}
}

// TestServerDirOfflineLayout is the regression test for the rc2/rc3 incident:
// with only server/ + migrations/ + livekit/, ServerDir() must return the
// install root so webhostDir() resolves to <root>/server/webhost/dist/<name>
// instead of <root>/server/server/webhost/dist/<name> (admin/web 404).
func TestServerDirOfflineLayout(t *testing.T) {
	root := t.TempDir()
	layoutOffline(t, root)
	chdir(t, filepath.Join(root, "server"))

	if got := ServerDir(); got != root {
		t.Fatalf("ServerDir() = %q, want %q (offline layout)", got, root)
	}
	wantWebhost := filepath.Join(root, "server", "webhost", "dist", "admin")
	if got := filepath.Join(ServerDir(), "server", "webhost", "dist", "admin"); got != wantWebhost {
		t.Fatalf("webhost path = %q, want %q", got, wantWebhost)
	}
}

// TestServerDirFallbackKeepsBinaryDirWhenNoMarker documents the preserved
// fallback behaviour: when neither layout marker is found, ServerDir() returns
// the directory containing the running binary (candidates[0]).
func TestServerDirFallbackKeepsBinaryDirWhenNoMarker(t *testing.T) {
	empty := t.TempDir()
	chdir(t, empty)

	got := ServerDir()
	want := executableDir()
	if want == "" {
		t.Skip("os.Executable() unavailable")
	}
	if got != want {
		t.Fatalf("ServerDir() = %q, want binary dir %q (no marker found)", got, want)
	}
}

// executableDir mirrors the candidate derivation inside ServerDir().
func executableDir() string {
	ex, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(ex); err == nil {
		return filepath.Dir(resolved)
	}
	return filepath.Dir(ex)
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd %q: %v", old, err)
		}
	})
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
