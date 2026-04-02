package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"slack-issue-bot/internal/config"
	"slack-issue-bot/internal/diagnosis"
	ghclient "slack-issue-bot/internal/github"
	slackclient "slack-issue-bot/internal/slack"
)

type Workflow struct {
	cfg         *config.Config
	slack       *slackclient.Client
	issueClient *ghclient.IssueClient
	repoCache   *ghclient.RepoCache
	diagEngine  *diagnosis.Engine
}

func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	issueClient *ghclient.IssueClient,
	repoCache *ghclient.RepoCache,
	diagEngine *diagnosis.Engine,
) *Workflow {
	return &Workflow{
		cfg:         cfg,
		slack:       slack,
		issueClient: issueClient,
		repoCache:   repoCache,
		diagEngine:  diagEngine,
	}
}

func (w *Workflow) HandleReaction(event slackclient.ReactionEvent) {
	ctx := context.Background()

	channelCfg, ok := w.cfg.Channels[event.ChannelID]
	if !ok {
		slog.Debug("channel not configured, ignoring", "channel", event.ChannelID)
		return
	}

	reactionCfg, ok := w.cfg.Reactions[event.Reaction]
	if !ok {
		slog.Debug("reaction not configured, ignoring", "reaction", event.Reaction)
		return
	}

	slog.Info("processing reaction event",
		"channel", event.ChannelID,
		"reaction", event.Reaction,
		"type", reactionCfg.Type,
		"repo", channelCfg.Repo,
	)

	message, err := w.slack.FetchMessage(event.ChannelID, event.MessageTS)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to read the original message: %v", err)
		return
	}

	reporter := w.slack.ResolveUser(event.UserID)
	channelName := w.slack.GetChannelName(event.ChannelID)

	repoPath, err := w.repoCache.EnsureRepo(channelCfg.Repo)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to access repo %s: %v", channelCfg.Repo, err)
		return
	}

	keywords := slackclient.ExtractKeywords(message)
	diagResp, diagErr := w.diagEngine.Diagnose(ctx, diagnosis.DiagnoseInput{
		Type:     reactionCfg.Type,
		Message:  message,
		RepoPath: repoPath,
		Keywords: keywords,
	})

	if diagErr != nil {
		slog.Warn("AI diagnosis failed, creating issue without diagnosis", "error", diagErr)
		w.slack.PostMessage(event.ChannelID, ":warning: AI diagnosis unavailable, creating issue without diagnosis")
	}

	parts := strings.SplitN(channelCfg.Repo, "/", 2)
	if len(parts) != 2 {
		w.notifyError(event.ChannelID, "Invalid repo format: %s (expected owner/repo)", channelCfg.Repo)
		return
	}
	owner, repo := parts[0], parts[1]

	labels := append(reactionCfg.IssueLabels, channelCfg.DefaultLabels...)

	issueInput := ghclient.IssueInput{
		Type:        reactionCfg.Type,
		TitlePrefix: reactionCfg.IssueTitlePrefix,
		Channel:     channelName,
		Reporter:    reporter,
		Message:     message,
		Labels:      labels,
		Diagnosis:   diagResp,
	}

	issueURL, err := w.issueClient.CreateIssue(ctx, owner, repo, issueInput)
	if err != nil {
		w.notifyError(event.ChannelID, "Failed to create GitHub issue: %v", err)
		return
	}

	msg := fmt.Sprintf(":white_check_mark: Issue created: %s", issueURL)
	if err := w.slack.PostMessage(event.ChannelID, msg); err != nil {
		slog.Error("failed to post issue URL to slack", "error", err)
	}

	slog.Info("workflow completed", "issueURL", issueURL)
}

func (w *Workflow) notifyError(channelID string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg))
}
