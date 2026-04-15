package main

import (
	"context"
	"log/slog"
	"strings"

	"agentdock/internal/bot"
	ghclient "agentdock/internal/github"
	slackclient "agentdock/internal/slack"
)

// agentRunnerAdapter wraps AgentRunner to satisfy worker.Runner interface.
type agentRunnerAdapter struct {
	runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return a.runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}

// repoCacheAdapter wraps RepoCache to satisfy worker.RepoProvider interface.
type repoCacheAdapter struct {
	cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
	repoPath, err := a.cache.EnsureRepo(cloneURL)
	if err != nil {
		return "", err
	}
	if branch != "" {
		if err := a.cache.Checkout(repoPath, branch); err != nil {
			return "", err
		}
	}
	return repoPath, nil
}

// slackPosterAdapter wraps slackclient.Client to satisfy bot.SlackPoster interface.
// SlackPoster.PostMessage has no return value, but Client.PostMessage returns error.
type slackPosterAdapter struct {
	client *slackclient.Client
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
	if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
		slog.Warn("failed to post slack message", "channel", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
	if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
		slog.Warn("failed to update slack message", "channel", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
