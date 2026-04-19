package configloader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigPath_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got, err := ResolveConfigPath("~/foo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo.yaml")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
