package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v60/github"
	"slack-issue-bot/internal/llm"
)

type IssueInput struct {
	Type        string
	TitlePrefix string
	Channel     string
	Reporter    string
	Message     string
	Labels      []string
	Diagnosis   llm.DiagnoseResponse
}

type IssueClient struct {
	client *gh.Client
}

func NewIssueClient(token string) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
	}
}

func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo string, input IssueInput) (string, error) {
	title := buildTitle(input)
	body := FormatIssueBody(input)

	var labels []string
	labels = append(labels, input.Labels...)

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &labels,
	})
	if err != nil {
		return "", fmt.Errorf("create issue: %w", err)
	}

	return issue.GetHTMLURL(), nil
}

func buildTitle(input IssueInput) string {
	title := input.Message
	if idx := strings.IndexAny(title, "\n\r"); idx != -1 {
		title = title[:idx]
	}
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	if input.TitlePrefix != "" {
		title = input.TitlePrefix + " " + title
	}
	return title
}

func FormatIssueBody(input IssueInput) string {
	var sb strings.Builder

	sb.WriteString("### Source\n")
	sb.WriteString(fmt.Sprintf("- **Slack Channel:** %s\n", input.Channel))
	sb.WriteString(fmt.Sprintf("- **Reporter:** @%s\n", input.Reporter))
	sb.WriteString(fmt.Sprintf("- **Original Message:** %s\n\n", input.Message))

	hasDiagnosis := input.Diagnosis.Summary != ""

	if !hasDiagnosis {
		// Lite mode: no LLM was called, but we may have file references from grep
		if len(input.Diagnosis.Files) > 0 {
			sb.WriteString("### Potentially Related Files\n\n")
			for _, f := range input.Diagnosis.Files {
				if f.LineNumber > 0 {
					sb.WriteString(fmt.Sprintf("- `%s:%d`\n", f.Path, f.LineNumber))
				} else {
					sb.WriteString(fmt.Sprintf("- `%s`\n", f.Path))
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString("### Handoff Spec\n\n")
		sb.WriteString("_No centralized AI diagnosis was run. Use the prompt below with your own AI to investigate:_\n\n")
		sb.WriteString("```\n")
		if input.Type == "bug" {
			sb.WriteString(fmt.Sprintf("Investigate this bug report in the codebase:\n\n\"%s\"\n\n", input.Message))
			sb.WriteString("Check the files listed above. Identify the root cause and suggest a fix.\n")
		} else {
			sb.WriteString(fmt.Sprintf("Analyze this feature request against the codebase:\n\n\"%s\"\n\n", input.Message))
			sb.WriteString("Check the files listed above. Identify where to implement and estimate complexity.\n")
		}
		sb.WriteString("```\n")
		return sb.String()
	}

	if input.Type == "bug" {
		sb.WriteString("### AI Diagnosis\n\n")
		sb.WriteString(fmt.Sprintf("**Possible Cause:**\n%s\n\n", input.Diagnosis.Summary))

		if len(input.Diagnosis.Files) > 0 {
			sb.WriteString("**Potentially Related Files:**\n")
			for _, f := range input.Diagnosis.Files {
				sb.WriteString(fmt.Sprintf("- `%s:%d` — %s\n", f.Path, f.LineNumber, f.Description))
			}
			sb.WriteString("\n")
		}

		if len(input.Diagnosis.Suggestions) > 0 {
			sb.WriteString("**Suggested Fix Direction:**\n")
			for i, s := range input.Diagnosis.Suggestions {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
			}
		}
	} else {
		sb.WriteString("### AI Analysis\n\n")
		sb.WriteString(fmt.Sprintf("**Existing Related Functionality:**\n%s\n\n", input.Diagnosis.Summary))

		if len(input.Diagnosis.Files) > 0 {
			sb.WriteString("**Suggested Implementation Location:**\n")
			for _, f := range input.Diagnosis.Files {
				sb.WriteString(fmt.Sprintf("- `%s:%d` — %s\n", f.Path, f.LineNumber, f.Description))
			}
			sb.WriteString("\n")
		}

		if input.Diagnosis.Complexity != "" {
			sb.WriteString(fmt.Sprintf("**Complexity Assessment:** %s\n\n", input.Diagnosis.Complexity))
		}

		if len(input.Diagnosis.Suggestions) > 0 {
			sb.WriteString("**Suggested Approach:**\n")
			for i, s := range input.Diagnosis.Suggestions {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
			}
		}
	}

	return sb.String()
}
