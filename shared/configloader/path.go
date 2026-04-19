package configloader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveConfigPath expands ~/ in the input and returns an absolute path.
// If in is empty, the caller is responsible for supplying a default.
func ResolveConfigPath(in string) (string, error) {
	if strings.HasPrefix(in, "~/") || in == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ~: %w", err)
		}
		in = filepath.Join(home, strings.TrimPrefix(in, "~/"))
	}
	return filepath.Abs(in)
}
