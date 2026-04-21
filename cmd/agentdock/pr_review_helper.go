package main

import (
	"github.com/spf13/cobra"
)

var prReviewHelperCmd = &cobra.Command{
	Use:    "pr-review-helper",
	Short:  "Internal helper invoked by the github-pr-review skill",
	Hidden: true,
	Long: "pr-review-helper hosts the deterministic parts of the PR review " +
		"workflow: repo fingerprinting and the validate-before-post step for " +
		"inline comments. The github-pr-review skill invokes these subcommands; " +
		"end users should not run them directly.",
}

func init() {
	rootCmd.AddCommand(prReviewHelperCmd)
}
