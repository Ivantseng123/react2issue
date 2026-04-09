package bot

import (
	"fmt"
	"strings"

	"slack-issue-bot/internal/config"
)

type ThreadMessage struct {
	User      string
	Timestamp string
	Text      string
}

type AttachmentInfo struct {
	Path string
	Name string
	Type string // "image", "text", "document"
}

type PromptInput struct {
	ThreadMessages   []ThreadMessage
	Attachments      []AttachmentInfo
	ExtraDescription string
	RepoPath         string
	Branch           string
	Prompt           config.PromptConfig
}

func BuildPrompt(input PromptInput) string {
	var sb strings.Builder

	sb.WriteString("## Task\n\n")
	sb.WriteString("Analyze the following thread conversation and triage against the specified codebase. ")
	sb.WriteString("Produce a report suitable as a GitHub issue body.\n\n")

	sb.WriteString("## Thread Context\n\n")
	for _, msg := range input.ThreadMessages {
		sb.WriteString(fmt.Sprintf("%s (%s):\n> %s\n\n", msg.User, msg.Timestamp, msg.Text))
	}

	if input.ExtraDescription != "" {
		sb.WriteString("## Extra Description\n\n")
		sb.WriteString(fmt.Sprintf("> %s\n\n", input.ExtraDescription))
	}

	sb.WriteString("## Repository\n\n")
	sb.WriteString(fmt.Sprintf("Path: %s\n", input.RepoPath))
	if input.Branch != "" {
		sb.WriteString(fmt.Sprintf("Branch: %s\n", input.Branch))
	}
	sb.WriteString("\n")

	if len(input.Attachments) > 0 {
		sb.WriteString("## Attachments\n\n")
		for _, att := range input.Attachments {
			hint := ""
			switch att.Type {
			case "image":
				hint = " (image — use your file reading tools to view)"
			case "text":
				hint = " (text — read directly)"
			case "document":
				hint = " (document)"
			}
			sb.WriteString(fmt.Sprintf("- %s%s\n", att.Path, hint))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Output Format\n\n")
	sb.WriteString("Output markdown first (used directly as the issue body),\n")
	sb.WriteString("then a ===TRIAGE_METADATA=== separator, then JSON:\n\n")
	sb.WriteString("```\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "issue_type": "bug|feature|improvement|question",` + "\n")
	sb.WriteString(`  "confidence": "low|medium|high",` + "\n")
	sb.WriteString(`  "files": [{"path": "...", "line": 0, "relevance": "..."}],` + "\n")
	sb.WriteString(`  "open_questions": [],` + "\n")
	sb.WriteString(`  "suggested_title": "..."` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("```\n\n")

	if input.Prompt.Language != "" {
		sb.WriteString(fmt.Sprintf("Response language: %s\n", input.Prompt.Language))
	}
	if len(input.Prompt.ExtraRules) > 0 {
		sb.WriteString("\nAdditional rules:\n")
		for _, rule := range input.Prompt.ExtraRules {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}

	return sb.String()
}
