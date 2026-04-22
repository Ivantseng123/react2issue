package workflow

import "fmt"

// Registry maps Job.TaskType → Workflow. It is populated at app startup and
// read by the dispatcher and ResultListener. Registration failures (empty or
// duplicate Type) panic at startup so misconfiguration is caught immediately.
type Registry struct {
	workflows map[string]Workflow
}

// NewRegistry returns an empty registry. Callers add workflows via Register.
func NewRegistry() *Registry {
	return &Registry{workflows: make(map[string]Workflow)}
}

// Register adds a workflow. Panics if the workflow reports an empty Type()
// or if a workflow with the same Type() is already registered.
func (r *Registry) Register(w Workflow) {
	t := w.Type()
	if t == "" {
		panic("workflow: Register called with empty Type()")
	}
	if _, exists := r.workflows[t]; exists {
		panic(fmt.Sprintf("workflow: duplicate registration for %q", t))
	}
	r.workflows[t] = w
}

// Get returns the workflow for the given task type. Callers that receive
// ok==false should surface "unknown task type" to the user — this is the
// natural enforcement point for the spec's "app-side dispatch" contract.
func (r *Registry) Get(taskType string) (Workflow, bool) {
	w, ok := r.workflows[taskType]
	return w, ok
}

// Types returns the registered workflow types in no particular order. Used
// for logging and by the D-selector to build its button list dynamically.
// Callers that need stable UI ordering should sort the result.
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.workflows))
	for t := range r.workflows {
		out = append(out, t)
	}
	return out
}
