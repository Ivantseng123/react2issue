package configloader

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/knadh/koanf/v2"
)

// DeltaInfo describes whether the config file existed and whether the user
// passed any flag overrides. Used to decide whether to trigger save-back.
type DeltaInfo struct {
	FileExisted     bool
	HadFlagOverride bool
}

// SaveConfig writes kSave to path if any delta condition is met:
//
//	A. preflight prompted any value (prompted non-empty), or
//	B. flag override happened (delta.HadFlagOverride), or
//	C. config file didn't exist (!delta.FileExisted).
//
// Returns (written, error). Skips the write when output is byte-identical
// to existing file.
func SaveConfig(kSave *koanf.Koanf, path string, prompted map[string]any, delta DeltaInfo) (bool, error) {
	shouldWrite := len(prompted) > 0 || delta.HadFlagOverride || !delta.FileExisted
	if !shouldWrite {
		return false, nil
	}
	for k, v := range prompted {
		if err := kSave.Set(k, v); err != nil {
			return false, fmt.Errorf("kSave.Set(%s): %w", k, err)
		}
	}
	parser, err := PickParser(path)
	if err != nil {
		return false, err
	}
	data, err := kSave.Marshal(parser)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}
	if err := AtomicWrite(path, data, 0600); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}
