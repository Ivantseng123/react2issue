package logging

import (
	"fmt"
	"os"
	"path/filepath"
)

// SaveAgentOutput writes agent raw output to a separate .md file.
// Returns the file path for logging reference.
func SaveAgentOutput(dir, requestID, repo, output string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create agent output dir: %w", err)
	}
	filename := requestID + ".md"
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(output), 0644); err != nil {
		return "", fmt.Errorf("write agent output: %w", err)
	}
	return path, nil
}
