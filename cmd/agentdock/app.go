package main

import (
	"fmt"
	"path/filepath"

	"github.com/Ivantseng123/agentdock/app"
	appconfig "github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/configloader"
	"github.com/Ivantseng123/agentdock/worker/pool"

	"github.com/spf13/cobra"
)

var (
	appConfigPath    string
	workerConfigPath string
)

var appCmd = &cobra.Command{
	Use:          "app",
	Short:        "Run the main Slack bot",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		appCfg, appPath, err := loadAppConfig(cmd, appConfigPath)
		if err != nil {
			return err
		}
		if err := appconfig.Validate(appCfg); err != nil {
			return err
		}
		if _, err := appconfig.RunPreflight(appCfg); err != nil {
			return fmt.Errorf("preflight: %w", err)
		}

		handle, err := app.Run(appCfg)
		if err != nil {
			return err
		}

		if appCfg.Queue.Transport != "redis" {
			wcfgPath := resolveWorkerConfigForInmem(appPath, workerConfigPath)
			wcfg, err := loadWorkerConfigForInmem(cmd, wcfgPath)
			if err != nil {
				return fmt.Errorf(
					"inmem mode requires worker configuration, but none found\n"+
						"  tried: %s\n"+
						"  run: agentdock init worker\n"+
						"  or:  agentdock app --worker-config /path/to/worker.yaml",
					wcfgPath)
			}
			workerLogger, githubLogger := handle.Loggers()
			if _, err := pool.StartLocal(wcfg, handle.Buses(), handle.Store(), githubLogger, workerLogger); err != nil {
				return fmt.Errorf("inmem worker pool start: %w", err)
			}
		}
		return handle.Wait()
	},
}

func init() {
	app.Version = version
	app.Commit = commit
	app.Date = date

	appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to app config file (default ~/.config/agentdock/app.yaml)")
	appCmd.Flags().StringVar(&workerConfigPath, "worker-config", "", "path to worker config file for inmem mode (default: sibling worker.yaml)")
	appconfig.RegisterFlags(appCmd)
	rootCmd.AddCommand(appCmd)
}

// resolveWorkerConfigForInmem picks the worker config path with this priority:
//  1. --worker-config flag, if set
//  2. worker.yaml sibling to the app config file
func resolveWorkerConfigForInmem(appPath, flagValue string) string {
	if flagValue != "" {
		resolved, err := configloader.ResolveConfigPath(flagValue)
		if err == nil {
			return resolved
		}
		return flagValue
	}
	return filepath.Join(filepath.Dir(appPath), "worker.yaml")
}
