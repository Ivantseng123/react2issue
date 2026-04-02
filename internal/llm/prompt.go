package llm

import (
	"fmt"
	"strings"
)

// PromptOptions holds user-configurable prompt settings from config.
type PromptOptions struct {
	Language   string
	ExtraRules []string
}

// AgentSystemPrompt builds the system prompt for the agent-loop diagnosis.
func AgentSystemPrompt(diagType string, opts PromptOptions) string {
	var sb strings.Builder

	sb.WriteString(`You are a code triage assistant. You have tools to investigate a repository and must produce a triage card.

Rules:
- Start with grep or search_code to find relevant code. Do NOT skip this step.
- If the report is in a non-English language, translate key terms to English identifiers before searching.
- Use read_context to understand the repo structure if initial searches miss.
- Read at most 5 files total — pick the most relevant ones.
- Do NOT guess variable names, field names, UI labels, or component positions.
- Set confidence to "low" if you cannot find clearly related code, "medium" if partially related, "high" if strongly related.
- open_questions: ONLY things the reporter/PM can answer (e.g. "which module?", "what screen?"). Do NOT include code-visibility issues like "file was truncated".

`)

	sb.WriteString(agentOutputSchema(diagType))

	if opts.Language != "" {
		sb.WriteString(fmt.Sprintf("\n\nRespond in %s. All JSON string values in %s.", opts.Language, opts.Language))
	}
	for _, rule := range opts.ExtraRules {
		sb.WriteString("\n" + rule)
	}
	return sb.String()
}

func agentOutputSchema(diagType string) string {
	if diagType == "bug" {
		return `When you have gathered enough information, respond with ONLY this JSON (no markdown fences):
{
  "summary": "One sentence: what area of code is likely involved",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant (one sentence)"}],
  "suggestions": ["What to investigate (max 2 items)"],
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
	}
	return `When you have gathered enough information, respond with ONLY this JSON (no markdown fences):
{
  "summary": "One sentence: what existing code relates to this request",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant (one sentence)"}],
  "suggestions": ["Where to start (max 2 items)"],
  "complexity": "low|medium|high",
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
}

// CLIToolPromptSuffix returns additional prompt text for CLI/Ollama providers
// that do not support native tool calling. It describes how to invoke tools
// using a JSON-in-text format.
func CLIToolPromptSuffix(tools []ToolDef) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Available Tools\n\n")
	sb.WriteString("To call a tool, write a line starting with TOOL_CALL followed by JSON:\n")
	sb.WriteString("TOOL_CALL {\"name\": \"<tool_name>\", \"args\": {<arguments>}}\n\n")
	sb.WriteString("Wait for the result before making the next call. Available tools:\n\n")

	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Name, t.Description))
	}
	return sb.String()
}
