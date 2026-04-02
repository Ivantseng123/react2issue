package llm

import "context"

// Backward-compatible types for the old single-turn diagnosis flow.
// Used by engine.go until Task 10 rewrites it to use ConversationProvider.
// TODO: remove after engine.go is rewritten (Task 10).

// DiagnoseRequest is the old single-turn request type.
type DiagnoseRequest struct {
	Type      string
	Message   string
	RepoFiles []File
	Prompt    PromptOptions
}

// Provider is the old single-turn diagnosis interface.
type Provider interface {
	Name() string
	Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error)
}
