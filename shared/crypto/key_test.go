package crypto

import (
	"strings"
	"testing"
)

func TestDecodeSecretKey_Valid(t *testing.T) {
	// 64 hex chars = 32 bytes.
	key := strings.Repeat("a", 64)
	decoded, err := DecodeSecretKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("decoded len = %d, want 32", len(decoded))
	}
}

func TestDecodeSecretKey_Invalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"short", "abcd"},
		{"non-hex", strings.Repeat("z", 64)},
		{"wrong length", strings.Repeat("a", 62)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecodeSecretKey(c.in); err == nil {
				t.Errorf("expected error for %q", c.in)
			}
		})
	}
}
