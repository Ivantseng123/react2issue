package config

import "testing"

func TestBuiltinAgents_HasExpected(t *testing.T) {
	expected := []string{"claude", "codex", "opencode"}
	for _, name := range expected {
		agent, ok := BuiltinAgents[name]
		if !ok {
			t.Errorf("BuiltinAgents missing %q", name)
			continue
		}
		if agent.Command == "" {
			t.Errorf("BuiltinAgents[%q].Command is empty", name)
		}
		if agent.SkillDir == "" {
			t.Errorf("BuiltinAgents[%q].SkillDir is empty", name)
		}
	}
}
