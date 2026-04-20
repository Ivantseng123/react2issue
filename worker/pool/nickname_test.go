package pool

import (
	"math/rand"
	"testing"
)

func TestPickNicknames_PoolLargerThanCount(t *testing.T) {
	pool := []string{"Alice", "Bob", "Charlie", "Delta", "Echo"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	seen := map[string]bool{}
	for i, n := range got {
		if n == "" {
			t.Errorf("got[%d] is empty, want a pool entry", i)
		}
		if seen[n] {
			t.Errorf("got[%d]=%q is a duplicate (pool ≥ count should not repeat)", i, n)
		}
		seen[n] = true
	}
	for n := range seen {
		found := false
		for _, p := range pool {
			if p == n {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("got %q, which is not in pool", n)
		}
	}
}

func TestPickNicknames_PoolEqualsCount(t *testing.T) {
	pool := []string{"Alice", "Bob", "Charlie"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, n := range got {
		if n == "" {
			t.Errorf("pool = count: no slot should be empty")
		}
		seen[n] = true
	}
	if len(seen) != 3 {
		t.Errorf("pool = count: every pool entry should appear exactly once, got seen=%v", seen)
	}
}

func TestPickNicknames_PoolSmallerThanCount(t *testing.T) {
	pool := []string{"Alice", "Bob"}
	got := pickNicknames(pool, 5, rand.New(rand.NewSource(42)))
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	nonEmpty := 0
	for _, n := range got {
		if n != "" {
			nonEmpty++
		}
	}
	if nonEmpty != 2 {
		t.Errorf("nonEmpty = %d, want 2 (pool size)", nonEmpty)
	}
}

func TestPickNicknames_EmptyPool(t *testing.T) {
	got := pickNicknames(nil, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, n := range got {
		if n != "" {
			t.Errorf("got[%d] = %q, want empty (empty pool)", i, n)
		}
	}
}

func TestPickNicknames_ZeroCount(t *testing.T) {
	got := pickNicknames([]string{"Alice"}, 0, rand.New(rand.NewSource(42)))
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestPickNicknames_DuplicateEntriesAllowed(t *testing.T) {
	// User may intentionally repeat a name in the pool.
	pool := []string{"Alice", "Alice", "Alice"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	for i, n := range got {
		if n != "Alice" {
			t.Errorf("got[%d] = %q, want Alice", i, n)
		}
	}
}

func TestPickNicknames_DeterministicForSameSeed(t *testing.T) {
	pool := []string{"A", "B", "C", "D", "E"}
	a := pickNicknames(pool, 3, rand.New(rand.NewSource(123)))
	b := pickNicknames(pool, 3, rand.New(rand.NewSource(123)))
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("seed 123 slot %d: first=%q, second=%q — non-deterministic", i, a[i], b[i])
		}
	}
}
