package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	initConfigPath  string
	initForce       bool
	initInteractive bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a starter config file",
	Long:  "Writes a starter config to the path specified by --config (default ~/.config/agentdock/config.yaml).",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("init not yet implemented; coming in Phase 5")
	},
}

func init() {
	initCmd.Flags().StringVarP(&initConfigPath, "config", "c", "", "path for new config file (default ~/.config/agentdock/config.yaml)")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite if file exists")
	initCmd.Flags().BoolVarP(&initInteractive, "interactive", "i", false, "prompt for required values")
	rootCmd.AddCommand(initCmd)
}
