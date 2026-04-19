package main

import (
	"fmt"
	"log/slog"
	"os"

	appconfig "github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/configloader"
	workerconfig "github.com/Ivantseng123/agentdock/worker/config"

	"github.com/spf13/cobra"
)

// loadAppConfig resolves the app config path, runs BuildKoanf, and writes
// any delta back to disk. Returns the loaded config plus the resolved path
// so callers can derive a sibling worker.yaml for inmem mode.
func loadAppConfig(cmd *cobra.Command, path string) (*appconfig.Config, string, error) {
	resolved, err := resolveAppConfigPath(path)
	if err != nil {
		return nil, "", err
	}
	cfg, _, kSave, delta, err := appconfig.BuildKoanf(cmd, resolved)
	if err != nil {
		return nil, resolved, err
	}
	if path != "" && !delta.FileExisted {
		return nil, resolved, fmt.Errorf("config file not found: %s; run 'agentdock init app -c %s' first", resolved, resolved)
	}
	if _, err := configloader.SaveConfig(kSave, resolved, nil, delta); err != nil {
		slog.Warn("設定儲存失敗", "phase", "失敗", "path", resolved, "error", err)
	}
	return cfg, resolved, nil
}

// loadWorkerConfig is the symmetric helper for `agentdock worker`.
func loadWorkerConfig(cmd *cobra.Command, path string) (*workerconfig.Config, string, error) {
	resolved, err := resolveWorkerConfigPath(path)
	if err != nil {
		return nil, "", err
	}
	cfg, _, kSave, delta, err := workerconfig.BuildKoanf(cmd, resolved)
	if err != nil {
		return nil, resolved, err
	}
	if path != "" && !delta.FileExisted {
		return nil, resolved, fmt.Errorf("config file not found: %s; run 'agentdock init worker -c %s' first", resolved, resolved)
	}
	if _, err := configloader.SaveConfig(kSave, resolved, nil, delta); err != nil {
		slog.Warn("設定儲存失敗", "phase", "失敗", "path", resolved, "error", err)
	}
	return cfg, resolved, nil
}

// loadWorkerConfigForInmem is the inmem-mode variant. It does not register a
// flag override map from the app cobra command (those flags are app-scoped)
// and returns an error when the file is missing so cmd/agentdock/app.go can
// emit a helpful hint telling the operator how to recover.
func loadWorkerConfigForInmem(_ *cobra.Command, path string) (*workerconfig.Config, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("worker config not found: %s", path)
		}
		return nil, err
	}
	// Build without cmd flags — inmem worker config comes from file only.
	emptyCmd := &cobra.Command{}
	workerconfig.RegisterFlags(emptyCmd)
	cfg, _, _, _, err := workerconfig.BuildKoanf(emptyCmd, path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// resolveAppConfigPath expands ~/ and returns an absolute path. Empty input
// falls back to ~/.config/agentdock/app.yaml.
func resolveAppConfigPath(in string) (string, error) {
	if in == "" {
		in = "~/.config/agentdock/app.yaml"
	}
	return configloader.ResolveConfigPath(in)
}

// resolveWorkerConfigPath expands ~/ and returns an absolute path. Empty
// input falls back to ~/.config/agentdock/worker.yaml.
func resolveWorkerConfigPath(in string) (string, error) {
	if in == "" {
		in = "~/.config/agentdock/worker.yaml"
	}
	return configloader.ResolveConfigPath(in)
}
