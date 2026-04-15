package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "agentdock",
	Short:   "AgentDock — Slack to GitHub issue triage",
	Long:    "AgentDock turns Slack threads into structured GitHub issues with AI-assisted triage.",
	Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
}

func init() {
	addPersistentFlags(rootCmd)
}

// Execute runs the root command. Called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
