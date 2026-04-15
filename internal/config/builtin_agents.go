package config

// BuiltinAgents is the canonical registry of agent CLI configurations shipped
// with AgentDock. Config files may override individual entries by defining a
// same-named entry under `agents:`; missing names fall back to these defaults (D16).
//
// Adding a new built-in agent: just add an entry here. Existing users get it
// automatically on next startup; no `agentdock init` rerun needed.
var BuiltinAgents = map[string]AgentConfig{
	"claude": {
		Command:  "claude",
		Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
		SkillDir: ".claude/skills",
		Stream:   true,
	},
	"codex": {
		Command:  "codex",
		Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
		SkillDir: ".codex/skills",
		Stream:   true,
	},
	"opencode": {
		Command:  "opencode",
		Args:     []string{"--prompt", "{prompt}"},
		SkillDir: ".opencode/skills",
	},
}
