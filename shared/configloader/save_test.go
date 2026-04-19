package configloader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

func TestSaveConfig_NoDelta_SkipsWrite(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{"x": 1}, "."), nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")
	os.WriteFile(path, []byte("x: 1\n"), 0600)

	written, err := SaveConfig(k, path, map[string]any{}, DeltaInfo{FileExisted: true})
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("expected skip when no delta")
	}
}

func TestSaveConfig_FlagOverride_Writes(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{"x": 2}, "."), nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")
	os.WriteFile(path, []byte("x: 1\n"), 0600)

	written, err := SaveConfig(k, path, map[string]any{}, DeltaInfo{FileExisted: true, HadFlagOverride: true})
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Error("expected write when flag override set")
	}
}
