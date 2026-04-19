package main

import (
	"github.com/Ivantseng123/agentdock/worker"

	"github.com/spf13/cobra"
)

var workerConfigPath string

var workerCmd = &cobra.Command{
	Use:          "worker",
	Short:        "Run a worker process (Redis mode)",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return loadAndStash(cmd, workerConfigPath, ScopeWorker)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return worker.Run(cfgFromCtx(cmd.Context()))
	},
}

func init() {
	workerCmd.Flags().StringVarP(&workerConfigPath, "config", "c", "",
		"path to worker config file (default ~/.config/agentdock/config.yaml)")
}
