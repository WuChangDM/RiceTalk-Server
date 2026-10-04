package database

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeSSLMode(t *testing.T) {
	clearEnv := func() {
		os.Unsetenv("RRT_PG_SSL_MODE")
		os.Unsetenv("RRT_ENV")
	}

	t.Run("production URL without sslmode defaults to require", func(t *testing.T) {
		clearEnv()
		os.Setenv("RRT_ENV", "production")
		mode, err := normalizeSSLMode("postgres://user:pass@localhost/db", "production", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != "require" {
			t.Errorf("mode = %q, want require", mode)
		}
	})

	t.Run("development URL without sslmode defaults to prefer", func(t *testing.T) {
		clearEnv()
		mode, err := normalizeSSLMode("postgres://user:pass@localhost/db", "development", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != "prefer" {
			t.Errorf("mode = %q, want prefer", mode)
		}
	})

	t.Run("explicit RRT_PG_SSL_MODE overrides URL", func(t *testing.T) {
		clearEnv()
		mode, err := normalizeSSLMode("postgres://user:pass@localhost/db?sslmode=disable", "production", "verify-full")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != "verify-full" {
			t.Errorf("mode = %q, want verify-full", mode)
		}
	})

	t.Run("production rejects explicit disable", func(t *testing.T) {
		clearEnv()
		_, err := normalizeSSLMode("postgres://user:pass@localhost/db", "production", "disable")
		if err == nil {
			t.Fatal("expected error for disable in production")
		}
	})

	t.Run("production rejects DSN disable", func(t *testing.T) {
		clearEnv()
		_, err := normalizeSSLMode("host=localhost dbname=db sslmode=disable", "production", "")
		if err == nil {
			t.Fatal("expected error for DSN disable in production")
		}
	})
}

func TestSetPostgresSSLMode(t *testing.T) {
	t.Run("URL form injects sslmode", func(t *testing.T) {
		dsn, err := setPostgresSSLMode("postgres://user:pass@localhost/db", "require")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(dsn, "sslmode=require") {
			t.Errorf("DSN %q missing sslmode=require", dsn)
		}
	})

	t.Run("URL form replaces existing sslmode", func(t *testing.T) {
		dsn, err := setPostgresSSLMode("postgres://user:pass@localhost/db?sslmode=disable", "require")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(dsn, "sslmode=disable") {
			t.Errorf("DSN still contains disable: %q", dsn)
		}
		if !strings.Contains(dsn, "sslmode=require") {
			t.Errorf("DSN %q missing sslmode=require", dsn)
		}
	})

	t.Run("key-value form injects sslmode", func(t *testing.T) {
		dsn, err := setPostgresSSLMode("host=localhost user=dbuser dbname=db", "require")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(dsn, "sslmode=require") {
			t.Errorf("DSN %q missing sslmode=require", dsn)
		}
	})

	t.Run("key-value form replaces existing sslmode", func(t *testing.T) {
		dsn, err := setPostgresSSLMode("host=localhost sslmode=disable dbname=db", "prefer")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(dsn, "sslmode=disable") {
			t.Errorf("DSN still contains disable: %q", dsn)
		}
		if !strings.Contains(dsn, "sslmode=prefer") {
			t.Errorf("DSN %q missing sslmode=prefer", dsn)
		}
	})
}

func TestOpenPostgres_ReturnsDialector(t *testing.T) {
	oldEnv := os.Getenv("RRT_ENV")
	oldMode := os.Getenv("RRT_PG_SSL_MODE")
	defer func() {
		os.Setenv("RRT_ENV", oldEnv)
		if oldMode == "" {
			os.Unsetenv("RRT_PG_SSL_MODE")
		} else {
			os.Setenv("RRT_PG_SSL_MODE", oldMode)
		}
	}()
	os.Unsetenv("RRT_PG_SSL_MODE")
	os.Setenv("RRT_ENV", "development")

	dialector, err := openPostgres("host=localhost user=u dbname=db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialector == nil {
		t.Fatal("expected dialector")
	}
}
