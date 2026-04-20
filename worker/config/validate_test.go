package config

import (
	"strings"
	"testing"
)

func baseValidCfg() *Config {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.RepoCache.Dir = "/tmp/agentdock-test"
	return cfg
}

func TestValidate_NicknamePool_EmptyPoolIsValid(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = nil
	if err := Validate(cfg); err != nil {
		t.Errorf("empty pool should be valid; got %v", err)
	}
	cfg.NicknamePool = []string{}
	if err := Validate(cfg); err != nil {
		t.Errorf("empty slice pool should be valid; got %v", err)
	}
}

func TestValidate_NicknamePool_SimpleEntriesValid(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"Alice", "小明", "Bob", "🧑‍💻"}
	if err := Validate(cfg); err != nil {
		t.Errorf("simple entries should be valid; got %v", err)
	}
}

func TestValidate_NicknamePool_TrimsLeadingTrailingWhitespace(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"  小明  ", "\tAlice\n"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("entries with trimmable whitespace should be valid; got %v", err)
	}
	if cfg.NicknamePool[0] != "小明" {
		t.Errorf("entry 0 = %q, want trimmed %q", cfg.NicknamePool[0], "小明")
	}
	if cfg.NicknamePool[1] != "Alice" {
		t.Errorf("entry 1 = %q, want trimmed %q", cfg.NicknamePool[1], "Alice")
	}
}

func TestValidate_NicknamePool_EmptyEntryFails(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"Alice", ""}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("empty entry should fail validation")
	}
	if !strings.Contains(err.Error(), "nickname_pool[1]") {
		t.Errorf("error should reference index 1; got %v", err)
	}
}

func TestValidate_NicknamePool_WhitespaceOnlyEntryFails(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"   "}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("whitespace-only entry should fail")
	}
	if !strings.Contains(err.Error(), "nickname_pool[0]") {
		t.Errorf("error should reference index 0; got %v", err)
	}
	if !strings.Contains(err.Error(), "empty or whitespace") {
		t.Errorf("error should say 'empty or whitespace'; got %v", err)
	}
}

func TestValidate_NicknamePool_OverLengthFails(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{strings.Repeat("a", 33)}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("33-rune entry should fail")
	}
	if !strings.Contains(err.Error(), "exceeds 32 runes") {
		t.Errorf("error should mention 'exceeds 32 runes'; got %v", err)
	}
}

func TestValidate_NicknamePool_ExactlyThirtyTwoRunesIsValid(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{strings.Repeat("中", 32)}
	if err := Validate(cfg); err != nil {
		t.Errorf("32-rune CJK entry should be valid; got %v", err)
	}
}

func TestValidate_NicknamePool_DangerousCharsAllowed(t *testing.T) {
	// <>& are not validation errors; they're handled at render time by slackEscape.
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"<@U123>", "A&B", "ok>"}
	if err := Validate(cfg); err != nil {
		t.Errorf("<>&- entries should be allowed at config layer; got %v", err)
	}
}

func TestValidate_NicknamePool_DuplicatesAllowed(t *testing.T) {
	cfg := baseValidCfg()
	cfg.NicknamePool = []string{"Alice", "Alice", "Bob"}
	if err := Validate(cfg); err != nil {
		t.Errorf("duplicate entries should be allowed; got %v", err)
	}
}
