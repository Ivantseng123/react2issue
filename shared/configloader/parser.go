// Package configloader provides pure helpers for loading yaml / json configs
// via koanf. It must not depend on any agentdock-specific config type.
package configloader

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
)

// PickParser returns the koanf parser matching a file extension.
// Only .yaml, .yml, and .json are supported.
func PickParser(path string) (koanf.Parser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".json":
		return json.Parser(), nil
	default:
		return nil, fmt.Errorf("unsupported config format: %s; only .yaml/.yml/.json supported", filepath.Ext(path))
	}
}
