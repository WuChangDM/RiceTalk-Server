package database

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// openPostgres opens a PostgreSQL dialector with safe SSL defaults.
//
// Behaviour:
//   - If RRT_PG_SSL_MODE is set, it overrides any sslmode value in the DSN.
//   - If the DSN already contains an sslmode value, it is preserved unless
//     the environment is production and the mode is "disable", which is rejected.
//   - In production (RRT_ENV=production), the default sslmode is "require".
//   - In all other environments, the default sslmode is "prefer".
func openPostgres(databaseURL string) (gorm.Dialector, error) {
	env := os.Getenv("RRT_ENV")
	if env == "" {
		env = "development"
	}

	explicitMode := os.Getenv("RRT_PG_SSL_MODE")

	mode, err := normalizeSSLMode(databaseURL, env, explicitMode)
	if err != nil {
		return nil, err
	}

	dsn, err := setPostgresSSLMode(databaseURL, mode)
	if err != nil {
		return nil, err
	}

	return postgres.Open(dsn), nil
}

// normalizeSSLMode decides the final sslmode value.
func normalizeSSLMode(databaseURL, env, explicitMode string) (string, error) {
	if explicitMode != "" {
		if !isValidSSLMode(explicitMode) {
			return "", fmt.Errorf("invalid RRT_PG_SSL_MODE value: %q", explicitMode)
		}
		if env == "production" && explicitMode == "disable" {
			return "", fmt.Errorf("RRT_PG_SSL_MODE=disable is not allowed in production")
		}
		return explicitMode, nil
	}

	existing, ok := extractSSLMode(databaseURL)
	if ok {
		if env == "production" && existing == "disable" {
			return "", fmt.Errorf("PostgreSQL sslmode=disable is not allowed in production; set sslmode=require or use RRT_PG_SSL_MODE")
		}
		return existing, nil
	}

	if env == "production" {
		fmt.Fprintln(os.Stderr, "INFO: PostgreSQL SSL defaulting to sslmode=require in production.")
		return "require", nil
	}
	return "prefer", nil
}

// isValidSSLMode reports whether v is a recognized libpq sslmode value.
func isValidSSLMode(v string) bool {
	switch v {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	}
	return false
}

// extractSSLMode returns the sslmode value already present in the DSN, if any.
func extractSSLMode(databaseURL string) (string, bool) {
	if looksLikePostgresURL(databaseURL) {
		u, err := url.Parse(databaseURL)
		if err != nil {
			return "", false
		}
		mode := u.Query().Get("sslmode")
		return mode, mode != ""
	}

	kvs := parsePostgresKV(databaseURL)
	mode, ok := kvs["sslmode"]
	return mode, ok
}

// setPostgresSSLMode injects or replaces sslmode in the DSN.
func setPostgresSSLMode(databaseURL, mode string) (string, error) {
	if looksLikePostgresURL(databaseURL) {
		u, err := url.Parse(databaseURL)
		if err != nil {
			return "", fmt.Errorf("parse postgres url: %w", err)
		}
		q := u.Query()
		q.Set("sslmode", mode)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}

	kvs := parsePostgresKV(databaseURL)
	kvs["sslmode"] = mode
	return buildPostgresKV(kvs), nil
}

// looksLikePostgresURL reports whether the DSN uses a postgres:// URL.
func looksLikePostgresURL(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

// parsePostgresKV parses a key=value style DSN.
// Values may be single-quoted; quotes are preserved in the returned map so that
// rebuild can emit them again if needed. This is a lightweight parser sufficient
// for libpq-style connection strings.
func parsePostgresKV(s string) map[string]string {
	result := make(map[string]string)
	var pairs []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' {
			inQuote = !inQuote
		}
		if ch == ' ' && !inQuote {
			if current.Len() > 0 {
				pairs = append(pairs, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		pairs = append(pairs, current.String())
	}

	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		result[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return result
}

// buildPostgresKV reconstructs a key=value DSN from a map.
// Values containing spaces are single-quoted.
func buildPostgresKV(kvs map[string]string) string {
	// Preserve a stable, predictable order for tests while still allowing the
	// caller to inspect the result.
	keys := make([]string, 0, len(kvs))
	for k := range kvs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := kvs[k]
		if strings.Contains(v, " ") && !strings.HasPrefix(v, "'") {
			v = strconv.Quote(v)
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, " ")
}
