package config

import (
	"time"

	internalconfig "github.com/Ivantseng123/agentdock/internal/config"
)

// BuiltinAgents is the canonical registry of agent CLI configurations shipped
// with AgentDock. Config files may override individual entries by defining a
// same-named entry under `agents:`; missing names fall back to these defaults (D16).
//
// Adding a new built-in agent: just add an entry here. Existing users get it
// automatically on next startup; no `agentdock init` rerun needed.
//
// Note: AgentConfig still lives in internal/config. Phase 4 will consolidate
// the type here or in shared — until then, this file uses internalconfig.AgentConfig
// to avoid duplicating the type definition.
var BuiltinAgents = map[string]internalconfig.AgentConfig{
	"claude": {
		Command:  "claude",
		Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".claude/skills",
		Stream:   true,
	},
	"codex": {
		Command:  "codex",
		Args:     []string{"exec", "--skip-git-repo-check", "--color", "never", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".codex/skills",
	},
	"opencode": {
		Command:  "opencode",
		Args:     []string{"run", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".opencode/skills",
	},
}
