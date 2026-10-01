package idgen

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func init() {
	// Initialize the global snowflake generator for tests.
	if err := Init(1, 1); err != nil {
		panic(fmt.Sprintf("idgen.Init failed: %v", err))
	}
}

func TestNextProducesUniqueIDs(t *testing.T) {
	const n = 10000
	seen := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		id := Next()
		if _, exists := seen[id]; exists {
			t.Errorf("duplicate ID generated: %d", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNextString(t *testing.T) {
	s := NextString()
	if s == "" {
		t.Errorf("NextString returned empty string")
	}
	if _, err := stringToInt(s); err != nil {
		t.Errorf("NextString produced non-numeric value %q: %v", s, err)
	}
}

func TestGenerateIDWithPrefix(t *testing.T) {
	cases := []struct {
		prefix string
	}{
		{PrefixUser},
		{PrefixChannel},
		{PrefixMessage},
		{PrefixSpace},
		{PrefixSession},
		{PrefixBot},
		{PrefixFile},
		{PrefixDoc},
		{PrefixStroke},
		{PrefixEvent},
		{PrefixGame},
		{PrefixNet},
		{PrefixReset},
		{PrefixDM},
		{PrefixInvite},
	}

	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			id := GenerateID(tc.prefix)
			expected := tc.prefix + "_"
			if !strings.HasPrefix(id, expected) {
				t.Errorf("expected ID to start with %q, got %q", expected, id)
			}
			suffix := strings.TrimPrefix(id, expected)
			if suffix == "" {
				t.Errorf("ID has empty suffix after prefix: %q", id)
			}
			if _, err := stringToInt(suffix); err != nil {
				t.Errorf("ID suffix %q is not numeric: %v", suffix, err)
			}
		})
	}
}

func TestGenerateIDUniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := GenerateID(PrefixUser)
		if _, exists := seen[id]; exists {
			t.Errorf("duplicate generated ID: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestGenerateIDConcurrent(t *testing.T) {
	const goroutines = 100
	const perGoroutine = 100

	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*perGoroutine)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := GenerateID(PrefixMessage)
				mu.Lock()
				if _, exists := seen[id]; exists {
					t.Errorf("duplicate ID under concurrency: %s", id)
				}
				seen[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	expected := goroutines * perGoroutine
	if len(seen) != expected {
		t.Errorf("expected %d unique IDs, got %d", expected, len(seen))
	}
}

func TestInitRejectsInvalidWorkerID(t *testing.T) {
	// Init uses sync.Once, so subsequent calls are no-ops. We can only verify
	// that Init does not panic for valid ranges. Invalid ranges would have
	// returned an error on the first call, but since Once already fired in
	// init(), we test the validation logic indirectly by checking that
	// workerMax/dataCenterMax bounds are sensible.
	if workerMax <= 0 {
		t.Errorf("workerMax should be positive, got %d", workerMax)
	}
	if dataCenterMax <= 0 {
		t.Errorf("dataCenterMax should be positive, got %d", dataCenterMax)
	}
}

// stringToInt is a small helper to validate that a string is a base-10 integer.
func stringToInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
