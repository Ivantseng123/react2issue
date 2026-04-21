package workflow

import "testing"

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	wf := &fakeWorkflow{typ: "issue"}
	r.Register(wf)

	got, ok := r.Get("issue")
	if !ok {
		t.Fatal("Get(issue) not found")
	}
	if got.Type() != "issue" {
		t.Errorf("got %q", got.Type())
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("unknown"); ok {
		t.Error("unknown task type should not be found")
	}
	if _, ok := r.Get(""); ok {
		t.Error("empty task type should not be found")
	}
}

func TestRegistry_RegisterEmptyTypePanic(t *testing.T) {
	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("expected panic on empty Type()")
		}
		msg, ok := v.(string)
		if !ok || msg != "workflow: Register called with empty Type()" {
			t.Errorf("unexpected panic value: %v", v)
		}
	}()
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: ""})
}

func TestRegistry_RegisterDuplicatePanic(t *testing.T) {
	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("expected panic on duplicate Type()")
		}
		want := `workflow: duplicate registration for "issue"`
		msg, ok := v.(string)
		if !ok || msg != want {
			t.Errorf("unexpected panic value: %v; want %q", v, want)
		}
	}()
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: "issue"})
	r.Register(&fakeWorkflow{typ: "issue"})
}
