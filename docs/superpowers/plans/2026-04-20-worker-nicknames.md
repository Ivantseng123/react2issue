# Worker Nicknames Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators set a pool of display nicknames in `worker.yaml`; each worker process picks one at startup (Fisher–Yates, no replacement when pool ≥ count); Slack status shows the nickname (or falls back to `worker-N`) with playful rewritten text and Slack mrkdwn escape.

**Architecture:** Three layers of change. (1) Pure logic — `pickNicknames` picker and `slackEscape` helper — added as isolated testable functions. (2) Data-flow plumbing — new `NicknamePool` config field, new `Nickname`/`WorkerNickname` fields on `WorkerInfo`/`StatusReport`, threaded through `pool.Config` → `statusAccumulator` → `StatusReport` → Slack renderer. (3) Display layer — `renderStatusMessage` rewritten with playful templates (「正在暖機」/「開工啦！」) and escape applied to user-controlled substrings.

**Tech Stack:** Go, `math/rand` for picks, `gopkg.in/yaml.v3` for config, stdlib `strings` / `unicode/utf8` for validation, existing `slog` logger.

**Spec:** `docs/superpowers/specs/2026-04-20-worker-nicknames-design.md` — reference this for decision rationale (Q1–Q12).

---

## Task 1: Create `pickNicknames` picker with unit tests

Pure logic, no deps. Fisher–Yates via `rng.Perm(len(pool))`: pool ≥ count → first `count` of permutation; pool < count → all of pool goes into `out[0:len(pool)]`, rest stay `""`; empty pool → all `""`.

**Files:**
- Create: `worker/pool/nickname.go`
- Create: `worker/pool/nickname_test.go`

- [ ] **Step 1: Write failing tests**

Create `worker/pool/nickname_test.go`:

```go
package pool

import (
	"math/rand"
	"testing"
)

func TestPickNicknames_PoolLargerThanCount(t *testing.T) {
	pool := []string{"Alice", "Bob", "Charlie", "Delta", "Echo"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	seen := map[string]bool{}
	for i, n := range got {
		if n == "" {
			t.Errorf("got[%d] is empty, want a pool entry", i)
		}
		if seen[n] {
			t.Errorf("got[%d]=%q is a duplicate (pool ≥ count should not repeat)", i, n)
		}
		seen[n] = true
	}
	for n := range seen {
		found := false
		for _, p := range pool {
			if p == n {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("got %q, which is not in pool", n)
		}
	}
}

func TestPickNicknames_PoolEqualsCount(t *testing.T) {
	pool := []string{"Alice", "Bob", "Charlie"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, n := range got {
		if n == "" {
			t.Errorf("pool = count: no slot should be empty")
		}
		seen[n] = true
	}
	if len(seen) != 3 {
		t.Errorf("pool = count: every pool entry should appear exactly once, got seen=%v", seen)
	}
}

func TestPickNicknames_PoolSmallerThanCount(t *testing.T) {
	pool := []string{"Alice", "Bob"}
	got := pickNicknames(pool, 5, rand.New(rand.NewSource(42)))
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	nonEmpty := 0
	for _, n := range got {
		if n != "" {
			nonEmpty++
		}
	}
	if nonEmpty != 2 {
		t.Errorf("nonEmpty = %d, want 2 (pool size)", nonEmpty)
	}
}

func TestPickNicknames_EmptyPool(t *testing.T) {
	got := pickNicknames(nil, 3, rand.New(rand.NewSource(42)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, n := range got {
		if n != "" {
			t.Errorf("got[%d] = %q, want empty (empty pool)", i, n)
		}
	}
}

func TestPickNicknames_ZeroCount(t *testing.T) {
	got := pickNicknames([]string{"Alice"}, 0, rand.New(rand.NewSource(42)))
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestPickNicknames_DuplicateEntriesAllowed(t *testing.T) {
	// User may intentionally repeat a name in the pool.
	pool := []string{"Alice", "Alice", "Alice"}
	got := pickNicknames(pool, 3, rand.New(rand.NewSource(42)))
	for i, n := range got {
		if n != "Alice" {
			t.Errorf("got[%d] = %q, want Alice", i, n)
		}
	}
}

func TestPickNicknames_DeterministicForSameSeed(t *testing.T) {
	pool := []string{"A", "B", "C", "D", "E"}
	a := pickNicknames(pool, 3, rand.New(rand.NewSource(123)))
	b := pickNicknames(pool, 3, rand.New(rand.NewSource(123)))
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("seed 123 slot %d: first=%q, second=%q — non-deterministic", i, a[i], b[i])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd worker && go test ./pool/ -run TestPickNicknames -v`
Expected: FAIL (`undefined: pickNicknames`)

- [ ] **Step 3: Implement `pickNicknames`**

Create `worker/pool/nickname.go`:

```go
package pool

import "math/rand"

// pickNicknames returns a slice of length count where each element is either
// a randomly selected pool entry (no replacement when pool >= count) or the
// empty string (when the pool runs out). Callers treat "" as "no nickname —
// fall back to the numeric worker-N label".
//
// Algorithm: Fisher–Yates via rng.Perm. The first min(len(pool), count)
// permuted indices are the picks; the remainder of out stays empty.
func pickNicknames(pool []string, count int, rng *rand.Rand) []string {
	out := make([]string, count)
	if count <= 0 || len(pool) == 0 {
		return out
	}
	perm := rng.Perm(len(pool))
	n := count
	if n > len(pool) {
		n = len(pool)
	}
	for i := 0; i < n; i++ {
		out[i] = pool[perm[i]]
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd worker && go test ./pool/ -run TestPickNicknames -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add worker/pool/nickname.go worker/pool/nickname_test.go
git commit -m "feat(worker): add pickNicknames for nickname pool selection

Pure Fisher-Yates picker. Pool >= count: no repeats. Pool < count:
remaining slots empty (caller falls back to worker-N). Seed-injectable
for deterministic tests."
```

---

## Task 2: Create `slackEscape` helper with unit tests

Pure string transform. Escapes `&`, `<`, `>` (the three Slack mrkdwn legacy XML entities). `&` MUST run first so the later `<`/`>` replacements don't get double-escaped by a second `&` pass.

**Files:**
- Modify: `app/bot/status_listener.go`
- Modify: `app/bot/status_listener_test.go`

- [ ] **Step 1: Write failing tests**

Add to `app/bot/status_listener_test.go` (append near `TestShortWorker`):

```go
func TestSlackEscape(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "小明", "小明"},
		{"lt only", "<heart>", "&lt;heart&gt;"},
		{"amp only", "A & B", "A &amp; B"},
		{"user mention neutralised", "<@U12345>", "&lt;@U12345&gt;"},
		{"channel broadcast neutralised", "<!channel>", "&lt;!channel&gt;"},
		{"amp before lt — no double-escape", "&<", "&amp;&lt;"},
		{"already-encoded stays idempotent-ish",
			"&amp;", "&amp;amp;"}, // we DO double-escape existing entities — that's correct for user input
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := slackEscape(c.in); got != c.want {
			t.Errorf("%s: slackEscape(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd app && go test ./bot/ -run TestSlackEscape -v`
Expected: FAIL (`undefined: slackEscape`)

- [ ] **Step 3: Implement `slackEscape`**

In `app/bot/status_listener.go`, add near the bottom (after `renderStatusMessage`):

```go
// slackEscape escapes the three characters Slack mrkdwn treats as legacy XML
// entities (<@U...>, <https://...>, etc.). Order matters: '&' runs first so
// the later '<' and '>' replacements don't get their ampersands double-escaped.
// Not using html.EscapeString because it also escapes ' and " into numeric
// entities that Slack mrkdwn does not reliably decode.
func slackEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
```

Verify the file already imports `"strings"` (it does — existing `strings.LastIndex` usage at `shortWorker`). No new import needed.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd app && go test ./bot/ -run TestSlackEscape -v`
Expected: PASS (1 test, 8 subcases)

- [ ] **Step 5: Commit**

```bash
git add app/bot/status_listener.go app/bot/status_listener_test.go
git commit -m "feat(app): add slackEscape for Slack mrkdwn safety

Escapes &, <, > only (the three chars Slack mrkdwn decodes as legacy
XML entities). Avoids html.EscapeString because its numeric entity
output for ' and \" has no guarantee of decoding in Slack mrkdwn."
```

---

## Task 3: Add `NicknamePool` field to worker `Config`

Pure type definition. No behavior yet — validation lands in Task 4.

**Files:**
- Modify: `worker/config/config.go`

- [ ] **Step 1: Add the field**

In `worker/config/config.go`, locate the `Config` struct (line 10) and add `NicknamePool` after `Count`:

```go
type Config struct {
	LogLevel     string                 `yaml:"log_level"`
	Logging      LoggingConfig          `yaml:"logging"`
	GitHub       GitHubConfig           `yaml:"github"`
	Agents       map[string]AgentConfig `yaml:"agents"`
	ActiveAgent  string                 `yaml:"active_agent"`
	Providers    []string               `yaml:"providers"`
	Count        int                    `yaml:"count"`
	NicknamePool []string               `yaml:"nickname_pool"`
	Prompt       PromptConfig           `yaml:"prompt"`
	RepoCache    RepoCacheConfig        `yaml:"repo_cache"`
	Queue        QueueConfig            `yaml:"queue"`
	Redis        RedisConfig            `yaml:"redis"`
	SecretKey    string                 `yaml:"secret_key"`
	Secrets      map[string]string      `yaml:"secrets"`
}
```

No `omitempty` — we want `agentdock init worker` to emit `nickname_pool: []` so operators see the key.

- [ ] **Step 2: Verify the whole worker module still builds**

Run: `cd worker && go build ./...`
Expected: no output (success)

- [ ] **Step 3: Verify init still produces a valid template**

Run: `cd worker && go test ./config/ -v`
Expected: PASS (existing tests unchanged)

- [ ] **Step 4: Commit**

```bash
git add worker/config/config.go
git commit -m "feat(worker): add NicknamePool to Config"
```

---

## Task 4: Validate `NicknamePool` (TrimSpace + length + whitespace)

TrimSpace-normalise each entry in place, then check `[1, 32]` rune length. Any failure is collected into the combined `Validate` error like the existing range-check pattern.

**Files:**
- Modify: `worker/config/validate.go`
- Modify: `worker/config/config_test.go` (or create `validate_test.go` — check below)

- [ ] **Step 1: Check whether `validate_test.go` exists**

Run: `ls worker/config/validate_test.go 2>&1 || echo MISSING`
If MISSING, we'll create it. If exists, append to it.

(At time of writing this plan: file does not exist. The plan assumes creation — adjust if the file appeared since.)

- [ ] **Step 2: Write failing tests**

Create `worker/config/validate_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd worker && go test ./config/ -run TestValidate_NicknamePool -v`
Expected: FAIL (tests pass unexpectedly because Validate currently ignores NicknamePool, OR some fail like whitespace trim)

- [ ] **Step 4: Extend `Validate`**

In `worker/config/validate.go`, add import and new block inside `Validate`. Replace the file contents with:

```go
package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Validate runs cross-field range checks on the merged Config and returns a
// single error listing every problem found.
func Validate(cfg *Config) error {
	var errs []string

	if cfg.Count < 1 {
		errs = append(errs, "count must be >= 1")
	}
	if cfg.Queue.Capacity < 1 {
		errs = append(errs, "queue.capacity must be >= 1")
	}
	if cfg.Queue.JobTimeout <= 0 {
		errs = append(errs, "queue.job_timeout must be > 0")
	}
	if cfg.Queue.AgentIdleTimeout <= 0 {
		errs = append(errs, "queue.agent_idle_timeout must be > 0")
	}
	if cfg.Queue.PrepareTimeout <= 0 {
		errs = append(errs, "queue.prepare_timeout must be > 0")
	}
	if cfg.Queue.StatusInterval <= 0 {
		errs = append(errs, "queue.status_interval must be > 0")
	}
	if cfg.Logging.RetentionDays < 1 {
		errs = append(errs, "logging.retention_days must be >= 1")
	}
	if cfg.RepoCache.Dir == "" {
		errs = append(errs, "repo_cache.dir must not be empty; delete the line to use the default, or set an absolute path")
	} else if !filepath.IsAbs(cfg.RepoCache.Dir) {
		errs = append(errs, fmt.Sprintf("repo_cache.dir must be an absolute path, got %q", cfg.RepoCache.Dir))
	}
	if cfg.RepoCache.MaxAge <= 0 {
		errs = append(errs, "repo_cache.max_age must be > 0")
	}

	// NicknamePool: trim-normalise in place, then check length.
	for i, raw := range cfg.NicknamePool {
		trimmed := strings.TrimSpace(raw)
		cfg.NicknamePool[i] = trimmed
		if trimmed == "" {
			errs = append(errs, fmt.Sprintf("nickname_pool[%d] is empty or whitespace", i))
			continue
		}
		if n := utf8.RuneCountInString(trimmed); n > 32 {
			errs = append(errs, fmt.Sprintf("nickname_pool[%d] length %d exceeds 32 runes", i, n))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd worker && go test ./config/ -run TestValidate_NicknamePool -v`
Expected: PASS (9 tests)

Run the whole config suite to ensure no regressions: `cd worker && go test ./config/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add worker/config/validate.go worker/config/validate_test.go
git commit -m "feat(worker): validate nickname_pool entries

Each entry is TrimSpace-normalised in place, then rejected if empty
or longer than 32 runes. <>& are left alone — handled at render time
by slackEscape. Duplicates are allowed (operator's choice)."
```

---

## Task 5: Add `Nickname`/`WorkerNickname` to shared queue types

Type-only change. No tests at this layer — behavior is tested in later tasks via integration (Redis round-trip, StatusReport render).

**Files:**
- Modify: `shared/queue/job.go`
- Modify: `shared/queue/interface.go`

- [ ] **Step 1: Add `Nickname` to `WorkerInfo`**

In `shared/queue/job.go`, locate the `WorkerInfo` struct (line 116) and replace:

```go
type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Nickname    string   `json:"nickname,omitempty"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	ConnectedAt time.Time
}
```

`omitempty` keeps old JSON in Redis (missing `nickname`) decoding cleanly to `Nickname == ""`, and new workers with no nickname don't add unnecessary JSON bytes.

- [ ] **Step 2: Add `WorkerNickname` to `StatusReport`**

In `shared/queue/interface.go`, locate the `StatusReport` struct (line 43) and add `WorkerNickname` after `WorkerID`:

```go
type StatusReport struct {
	JobID          string    `json:"job_id"`
	WorkerID       string    `json:"worker_id"`
	WorkerNickname string    `json:"worker_nickname,omitempty"`
	PID            int       `json:"pid"`
	AgentCmd       string    `json:"agent_cmd"`
	Alive          bool      `json:"alive"`
	LastEvent      string    `json:"last_event,omitempty"`
	LastEventAt    time.Time `json:"last_event_at"`
	ToolCalls      int       `json:"tool_calls"`
	FilesRead      int       `json:"files_read"`
	OutputBytes    int       `json:"output_bytes"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	InputTokens    int       `json:"input_tokens,omitempty"`
	OutputTokens   int       `json:"output_tokens,omitempty"`
	PrepareSeconds float64   `json:"prepare_seconds,omitempty"`
	JobStatus      JobStatus `json:"job_status,omitempty"`
}
```

- [ ] **Step 3: Verify shared builds**

Run: `cd shared && go build ./...`
Expected: no output

- [ ] **Step 4: Verify existing tests still pass**

Run: `cd shared && go test ./queue/... -short`
Expected: PASS (Redis integration tests skip in -short; unit tests pass)

- [ ] **Step 5: Commit**

```bash
git add shared/queue/job.go shared/queue/interface.go
git commit -m "feat(queue): add Nickname/WorkerNickname fields

WorkerInfo.Nickname flows into Redis via Register; StatusReport.
WorkerNickname piggybacks on existing status reports so the app pod
can render it without a separate Redis lookup. Both omitempty for
rolling-upgrade compatibility."
```

---

## Task 6: Thread nickname through `statusAccumulator` and `toReport`

Accumulator carries the per-job nickname immutably. `toReport` emits it.

**Files:**
- Modify: `worker/pool/status.go`

- [ ] **Step 1: Add nickname field**

In `worker/pool/status.go`, locate `statusAccumulator` (line 10) and add `nickname`:

```go
type statusAccumulator struct {
	mu           sync.Mutex
	jobID        string
	workerID     string
	nickname     string
	pid          int
	agentCmd     string
	alive        bool
	lastEvent    string
	lastEventAt  time.Time
	toolCalls    int
	filesRead    int
	outputBytes  int
	costUSD        float64
	inputTokens    int
	outputTokens   int
	prepareSeconds float64
}
```

- [ ] **Step 2: Emit in `toReport`**

In the same file, locate `toReport` (line 64) and add `WorkerNickname`:

```go
func (s *statusAccumulator) toReport() queue.StatusReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queue.StatusReport{
		JobID:          s.jobID,
		WorkerID:       s.workerID,
		WorkerNickname: s.nickname,
		PID:            s.pid,
		AgentCmd:       s.agentCmd,
		Alive:          s.alive,
		LastEvent:      s.lastEvent,
		LastEventAt:    s.lastEventAt,
		ToolCalls:      s.toolCalls,
		FilesRead:      s.filesRead,
		OutputBytes:    s.outputBytes,
		CostUSD:        s.costUSD,
		InputTokens:    s.inputTokens,
		OutputTokens:   s.outputTokens,
		PrepareSeconds: s.prepareSeconds,
	}
}
```

- [ ] **Step 3: Verify builds**

Run: `cd worker && go build ./pool/`
Expected: no output

- [ ] **Step 4: Run pool tests**

Run: `cd worker && go test ./pool/ -short -v`
Expected: PASS (existing tests unchanged — nickname field defaults to empty string, preserving current behavior)

- [ ] **Step 5: Commit**

```bash
git add worker/pool/status.go
git commit -m "feat(pool): statusAccumulator carries nickname"
```

---

## Task 7: Thread `Nicknames []string` through `pool.Config` and executors

`Pool.Config` gains a slice of nicknames aligned to worker index. `workerHeartbeat` puts each into `WorkerInfo.Nickname`. `executeWithTracking` seeds the accumulator.

**Files:**
- Modify: `worker/pool/pool.go`

- [ ] **Step 1: Add `Nicknames` to `Config`**

In `worker/pool/pool.go`, locate `Config` (line 13) and add the field:

```go
type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	Nicknames      []string
	Hostname       string
	SkillDirs      []string
	Commands       queue.CommandBus
	Status         queue.StatusBus
	StatusInterval time.Duration
	Logger         *slog.Logger
	SecretKey      []byte
	WorkerSecrets  map[string]string
	ExtraRules     []string
}
```

Callers may pass `nil`; bounds-check before indexing (Step 2/3).

- [ ] **Step 2: Add a bounds-safe lookup helper**

At the bottom of `worker/pool/pool.go` (after `publishStatus`), add:

```go
// nicknameForIndex returns p.cfg.Nicknames[i] if it's in range, or "" otherwise.
// Tolerant of nil / short slices so tests that don't pre-fill nicknames still work.
func (p *Pool) nicknameForIndex(i int) string {
	if i < 0 || i >= len(p.cfg.Nicknames) {
		return ""
	}
	return p.cfg.Nicknames[i]
}
```

- [ ] **Step 3: Use nickname in `executeWithTracking`**

In `worker/pool/pool.go`, locate `executeWithTracking` (line 126). Replace the block that constructs `status` (line 143–147):

```go
	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: wID,
		nickname: p.nicknameForIndex(workerIndex),
		alive:    true,
	}
```

- [ ] **Step 4: Use nickname in `workerHeartbeat` initial Register**

In `worker/pool/pool.go`, locate `workerHeartbeat` (line 237). Replace the initial registration loop:

```go
func (p *Pool) workerHeartbeat(ctx context.Context) {
	now := time.Now()
	for i := 0; i < p.cfg.WorkerCount; i++ {
		info := queue.WorkerInfo{
			WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
			Name:        p.cfg.Hostname,
			Nickname:    p.nicknameForIndex(i),
			ConnectedAt: now,
		}
		if err := p.cfg.Queue.Register(ctx, info); err != nil {
			p.cfg.Logger.Warn("Worker 註冊失敗", "phase", "失敗", "worker_id", info.WorkerID, "error", err)
		}
	}
```

- [ ] **Step 5: Use nickname in the ticker re-registration**

Same function, replace the ticker branch:

```go
		case <-ticker.C:
			for i := 0; i < p.cfg.WorkerCount; i++ {
				info := queue.WorkerInfo{
					WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
					Name:        p.cfg.Hostname,
					Nickname:    p.nicknameForIndex(i),
					ConnectedAt: now,
				}
				p.cfg.Queue.Register(ctx, info)
			}
```

The unregister branch (ctx.Done) doesn't need the nickname — keep as-is.

- [ ] **Step 6: Run pool tests**

Run: `cd worker && go test ./pool/ -short -v`
Expected: PASS (existing tests pass `Nicknames: nil` implicitly; `nicknameForIndex` returns "" safely)

- [ ] **Step 7: Verify app build still compiles** (other packages import `queue.WorkerInfo`)

Run: `cd app && go build ./...`
Expected: no output

- [ ] **Step 8: Commit**

```bash
git add worker/pool/pool.go
git commit -m "feat(pool): thread Nicknames[] into Config and executors

Nicknames is a length=WorkerCount slice; slot i's nickname is written
to both WorkerInfo.Nickname (Redis registration) and the job's
statusAccumulator (so StatusReport carries it). nicknameForIndex is
bounds-safe so tests that omit Nicknames stay green."
```

---

## Task 8: Call `pickNicknames` from `worker.Run` and emit startup warn

The composition root reads the pool, seeds a fresh RNG with nanosecond time, picks, and drops the result into `pool.Config`. A warn log surfaces the pool<count case.

**Files:**
- Modify: `worker/worker.go`

- [ ] **Step 1: Export a thin wrapper from `worker/pool`**

`worker.go` lives in `package worker`, NOT `package pool`, so it can't call lowercase `pickNicknames`. Add an exported wrapper at the bottom of `worker/pool/nickname.go`:

```go
// PickNicknames is the exported wrapper around pickNicknames for use by
// worker.Run. Keeps the core algorithm package-private for tests while
// exposing a single call site.
func PickNicknames(pool []string, count int, rng *rand.Rand) []string {
	return pickNicknames(pool, count, rng)
}
```

- [ ] **Step 2: Wire up `pool.PickNicknames` in `worker.Run`**

In `worker/worker.go`, locate the block that builds `workerPool` (around line 88). Insert the pick + warn BEFORE the `pool.NewPool` call, and add `Nicknames: nicknames` to the `pool.Config` literal:

```go
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	if n := len(cfg.NicknamePool); n > 0 && n < cfg.Count {
		appLogger.Warn("nickname 池小於 worker 數，部份 worker 將無暱稱",
			"phase", "處理中", "pool_size", n, "worker_count", cfg.Count)
	}
	nicknameRNG := rand.New(rand.NewSource(time.Now().UnixNano()))
	nicknames := pool.PickNicknames(cfg.NicknamePool, cfg.Count, nicknameRNG)

	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	workerPool := pool.NewPool(pool.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &pool.AgentRunnerAdapter{Runner: agentRunner},
		RepoCache:      repoAdapter,
		WorkerCount:    cfg.Count,
		Nicknames:      nicknames,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
		Logger:         workerLogger,
		SecretKey:      secretKey,
		WorkerSecrets:  cfg.Secrets,
		ExtraRules:     cfg.Prompt.ExtraRules,
	})
```

- [ ] **Step 3: Add missing imports to `worker.go`**

Add `math/rand` to `worker/worker.go` imports:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ivantseng123/agentdock/shared/crypto"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/config"
	"github.com/Ivantseng123/agentdock/worker/pool"
)
```

If `time` wasn't already there, it probably was (used by existing logic). Verify.

- [ ] **Step 4: Verify worker builds**

Run: `cd worker && go build ./...`
Expected: no output

- [ ] **Step 5: Verify whole repo builds**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go build ./... && (cd app && go build ./...) && (cd shared && go build ./...)`
Expected: no output

- [ ] **Step 6: Run worker tests**

Run: `cd worker && go test ./... -short`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add worker/worker.go worker/pool/nickname.go
git commit -m "feat(worker): pick nicknames at startup and warn on undersized pool

worker.Run seeds a fresh math/rand from time.Now().UnixNano(), calls
pool.PickNicknames, and populates pool.Config.Nicknames. When pool
is non-empty but smaller than count, emits an Info-level warn log
so operators don't debug silently missing nicknames."
```

---

## Task 9: Add `formatWorkerLabel` helper

Tiny dispatcher: nickname wins when non-empty; otherwise fall back to the existing `shortWorker` of the WorkerID.

**Files:**
- Modify: `app/bot/status_listener.go`
- Modify: `app/bot/status_listener_test.go`

- [ ] **Step 1: Write failing tests**

Append to `app/bot/status_listener_test.go`:

```go
func TestFormatWorkerLabel(t *testing.T) {
	cases := []struct {
		name, workerID, nickname, want string
	}{
		{"nickname wins", "host/worker-0", "小明", "小明"},
		{"empty nickname falls back to shortWorker", "host/worker-2", "", "worker-2"},
		{"empty nickname empty workerID falls back to empty", "", "", ""},
		{"nickname beats even multi-slash workerID", "k8s/pod/worker-5", "Alice", "Alice"},
	}
	for _, c := range cases {
		if got := formatWorkerLabel(c.workerID, c.nickname); got != c.want {
			t.Errorf("%s: formatWorkerLabel(%q, %q) = %q, want %q", c.name, c.workerID, c.nickname, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd app && go test ./bot/ -run TestFormatWorkerLabel -v`
Expected: FAIL (`undefined: formatWorkerLabel`)

- [ ] **Step 3: Implement `formatWorkerLabel`**

In `app/bot/status_listener.go`, add near `shortWorker` (around line 160):

```go
// formatWorkerLabel returns the label to display for a worker in human-facing
// contexts (Slack status). Nickname wins when non-empty; otherwise falls back
// to shortWorker so the raw hostname/worker-N form still yields worker-N.
func formatWorkerLabel(workerID, nickname string) string {
	if nickname != "" {
		return nickname
	}
	return shortWorker(workerID)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd app && go test ./bot/ -run TestFormatWorkerLabel -v`
Expected: PASS (1 test, 4 subcases)

- [ ] **Step 5: Commit**

```bash
git add app/bot/status_listener.go app/bot/status_listener_test.go
git commit -m "feat(app): add formatWorkerLabel (nickname wins, else shortWorker)"
```

---

## Task 10: Rewrite `renderStatusMessage` with playful templates + escape

Replaces the existing `:gear: 準備中 · worker-0` style with `:toolbox: 小明 正在暖機...` / `:fire: 小明 開工啦！(claude) · 奮鬥 1m23s` / personalised stats line. Escapes both label and agent_cmd via `slackEscape`.

**Files:**
- Modify: `app/bot/status_listener.go`
- Modify: `app/bot/status_listener_test.go`

- [ ] **Step 1: Update the existing render tests to the new expected output**

In `app/bot/status_listener_test.go`, replace `TestRenderStatusMessage_Preparing`, `TestRenderStatusMessage_RunningNoStats`, `TestRenderStatusMessage_RunningWithStats`, `TestRenderStatusMessage_RunningElapsedZeroWhenStartedAtUnset`, and `TestRenderStatusMessage_RunningEmptyAgentCmd`:

```go
func TestRenderStatusMessage_Preparing(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 0}
	got := renderStatusMessage(state, r, "preparing")
	want := ":toolbox: worker-0 正在暖機..."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStatusMessage_PreparingWithNickname(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", WorkerNickname: "小明", PID: 0}
	got := renderStatusMessage(state, r, "preparing")
	want := ":toolbox: 小明 正在暖機..."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStatusMessage_RunningNoStats(t *testing.T) {
	started := time.Now().Add(-2*time.Minute - 15*time.Second)
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: started}
	r := queue.StatusReport{
		WorkerID: "host/worker-0",
		PID:      1234,
		AgentCmd: "codex",
	}
	got := renderStatusMessage(state, r, "running")
	// Allow ±1s drift on elapsed since test clock races.
	want14 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m14s"
	want15 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m15s"
	want16 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m16s"
	if got != want14 && got != want15 && got != want16 {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestRenderStatusMessage_RunningWithStats(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{
		WorkerID:       "host/worker-0",
		WorkerNickname: "小明",
		PID:            1234,
		AgentCmd:       "claude",
		ToolCalls:      15,
		FilesRead:      8,
	}
	got := renderStatusMessage(state, r, "running")
	if !containsBoth(got, ":fire: 小明 開工啦！(claude)", "小明 已經敲了 15 次工具、翻了 8 份檔") {
		t.Errorf("missing expected substrings: %q", got)
	}
}

func TestRenderStatusMessage_RunningElapsedZeroWhenStartedAtUnset(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning} // StartedAt zero
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "claude"}
	got := renderStatusMessage(state, r, "running")
	if got != ":fire: worker-0 開工啦！(claude)" {
		t.Errorf("should omit elapsed when StartedAt is zero: %q", got)
	}
}

func TestRenderStatusMessage_RunningEmptyAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: ""}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, ":fire: worker-0 開工啦！(agent)") {
		t.Errorf("should fall back to 'agent' placeholder: %q", got)
	}
}

func TestRenderStatusMessage_EscapesNickname(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", WorkerNickname: "<@U123>"}
	got := renderStatusMessage(state, r, "preparing")
	if !contains(got, "&lt;@U123&gt;") {
		t.Errorf("nickname should be escaped: %q", got)
	}
	if contains(got, "<@U123>") {
		t.Errorf("raw mention syntax leaked: %q", got)
	}
}

func TestRenderStatusMessage_EscapesAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "foo&bar"}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, "(foo&amp;bar)") {
		t.Errorf("agent_cmd should be escaped: %q", got)
	}
}
```

Also update the two `maybeUpdateSlack` tests that assert Chinese phase words:

Replace the body of `TestMaybeUpdateSlack_PreparingPhase`:

```go
	if !contains(c.Text, "正在暖機") || !contains(c.Text, "worker-0") {
		t.Errorf("text missing expected markers: %q", c.Text)
	}
```

Replace the assertion in `TestMaybeUpdateSlack_RunningWithToolCalls`:

```go
	if !containsBoth(slack.calls[0].Text, ":fire: worker-0 開工啦！(claude)", "已經敲了 15 次工具") {
		t.Errorf("missing expected substrings: %q", slack.calls[0].Text)
	}
```

Replace the assertion in `TestMaybeUpdateSlack_RunningNoToolCalls`:

```go
	if contains(slack.calls[0].Text, "已經敲了") {
		t.Errorf("should NOT include tool-call line for codex: %q", slack.calls[0].Text)
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd app && go test ./bot/ -run TestRenderStatusMessage -v`
Expected: FAIL (all the new/updated assertions fail because renderStatusMessage still emits old templates)

- [ ] **Step 3: Rewrite `renderStatusMessage`**

In `app/bot/status_listener.go`, locate `renderStatusMessage` (line 192) and replace:

```go
func renderStatusMessage(state *queue.JobState, r queue.StatusReport, phase string) string {
	label := slackEscape(formatWorkerLabel(r.WorkerID, r.WorkerNickname))

	agent := r.AgentCmd
	if agent == "" {
		agent = "agent"
	}
	agent = slackEscape(agent)

	switch phase {
	case "preparing":
		return fmt.Sprintf(":toolbox: %s 正在暖機...", label)
	case "running":
		var suffix string
		if !state.StartedAt.IsZero() {
			suffix = fmt.Sprintf(" · 奮鬥 %s", formatElapsed(time.Since(state.StartedAt)))
		}
		base := fmt.Sprintf(":fire: %s 開工啦！(%s)%s", label, agent, suffix)
		if r.ToolCalls > 0 || r.FilesRead > 0 {
			base += fmt.Sprintf("\n%s 已經敲了 %d 次工具、翻了 %d 份檔",
				label, r.ToolCalls, r.FilesRead)
		}
		return base
	}
	return ""
}
```

- [ ] **Step 4: Run all `bot/` tests**

Run: `cd app && go test ./bot/ -v`
Expected: PASS (all new + updated tests green; no regressions elsewhere)

- [ ] **Step 5: Commit**

```bash
git add app/bot/status_listener.go app/bot/status_listener_test.go
git commit -m "feat(app): playful status text + Slack escape

準備中 → :toolbox: {label} 正在暖機...
處理中 → :fire: {label} 開工啦！({agent}) · 奮鬥 {elapsed}
Stats line personalised with {label}. Both label and agent are
slackEscape'd so operator-provided <@U...> or & stays safe."
```

---

## Task 11: Redis integration — nickname round-trip

Existing `TestRedisJobQueue_WorkerRegistration` covers Register/ListWorkers. Extend it with Nickname.

**Files:**
- Modify: `shared/queue/redis_jobqueue_test.go`

- [ ] **Step 1: Extend the test**

In `shared/queue/redis_jobqueue_test.go`, locate `TestRedisJobQueue_WorkerRegistration` (line 186). Update the `worker := WorkerInfo{...}` block to include a nickname, and assert it round-trips:

```go
func TestRedisJobQueue_WorkerRegistration(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	worker := WorkerInfo{
		WorkerID:    "worker-test-001",
		Name:        "test-worker",
		Nickname:    "Alice",
		Agents:      []string{"claude", "codex"},
		Tags:        []string{"gpu", "fast"},
		ConnectedAt: time.Now(),
	}

	if err := q.Register(ctx, worker); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	workers, err := q.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("ListWorkers failed: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("ListWorkers count = %d, want 1", len(workers))
	}
	if workers[0].WorkerID != worker.WorkerID {
		t.Errorf("WorkerID = %q, want %q", workers[0].WorkerID, worker.WorkerID)
	}
	if workers[0].Name != worker.Name {
		t.Errorf("Name = %q, want %q", workers[0].Name, worker.Name)
	}
	if workers[0].Nickname != worker.Nickname {
		t.Errorf("Nickname = %q, want %q", workers[0].Nickname, worker.Nickname)
	}
	if len(workers[0].Agents) != 2 {
		t.Errorf("Agents len = %d, want 2", len(workers[0].Agents))
	}
}
```

(Keep the rest of the function as-is — just splice in the Nickname field and one assertion.)

- [ ] **Step 2: Run the integration test (requires Redis)**

If Redis is running locally on the default port:

Run: `cd shared && go test ./queue/ -run TestRedisJobQueue_WorkerRegistration -v`
Expected: PASS

If no Redis: the test will be skipped by `testRedisClient`. Still worth verifying: `cd shared && go test ./queue/ -run TestRedisJobQueue_WorkerRegistration -v` → test SKIP or PASS, never FAIL.

- [ ] **Step 3: Commit**

```bash
git add shared/queue/redis_jobqueue_test.go
git commit -m "test(queue): assert Nickname round-trips through Redis"
```

---

## Task 12: Update user docs

Add `nickname_pool` to both `configuration-worker.md` (zh) and `configuration-worker.en.md`. Also document the playful status text briefly.

**Files:**
- Modify: `docs/configuration-worker.md`
- Modify: `docs/configuration-worker.en.md`

- [ ] **Step 1: Update zh schema sample**

In `docs/configuration-worker.md`, locate the line `count: 3` (around line 44) and insert after it:

```yaml
count: 3                              # worker goroutine 數（扁平！舊是 worker.count）

nickname_pool: ["小明", "Alice", "Gary"]  # 可選：每個 worker 啟動時隨機抽一個當 Slack 顯示名
```

Then add a new subsection after the `## Schema` block (before `## Agent Stream`):

```markdown
## Worker Nicknames（選用）

`nickname_pool` 是一個字串池，worker process 啟動時從中隨機抽 `count` 個不重複的當 Slack 狀態顯示的暱稱。

- 池 **≥** count：每個 worker 各抽一個不重複條目（Fisher–Yates）。
- 池 **<** count：池裡的每個都會被用到一次，剩下的 worker 回退到 `worker-0` / `worker-1` 的機械名。
- 池為空或省略：全部 worker 都顯示 `worker-N`（跟現行行為一致）。
- 每個條目 1–32 runes，前後空白會自動 trim，**允許重複**（池裡填兩個 `"小明"` 就有機會兩個 worker 都叫小明）。
- 暱稱裡的 `<`、`>`、`&` 會在渲染到 Slack 時自動 escape，所以把 `<@U123>` 塞進池不會意外 ping 到人。

Slack 狀態訊息會從冷冰冰的 `:gear: 準備中 · worker-0` 改為擬人化版本：

- 準備階段：`:toolbox: 小明 正在暖機...`
- 執行中：`:fire: 小明 開工啦！(claude) · 奮鬥 1m23s`
- 統計行：`小明 已經敲了 15 次工具、翻了 8 份檔`

沒設暱稱時 `worker-N` 還是會套用同樣的句型（robot-worker 人設）。
```

- [ ] **Step 2: Update en schema sample**

In `docs/configuration-worker.en.md`, do the equivalent edits. Locate the `count:` line in the YAML block and insert:

```yaml
count: 3                              # worker goroutine count

nickname_pool: ["Alice", "Bob", "Gary"]  # optional: random display nicknames drawn once at startup
```

Add a matching `## Worker Nicknames (optional)` section:

```markdown
## Worker Nicknames (optional)

`nickname_pool` is a list of display names. At startup each worker process randomly picks one (Fisher–Yates, no replacement when `len(pool) >= count`).

- Pool **≥** count: every worker gets a distinct entry.
- Pool **<** count: pool is exhausted, remaining workers fall back to `worker-0`, `worker-1`, ...
- Empty or absent pool: all workers display `worker-N` (current behavior).
- Each entry is 1–32 runes; leading/trailing whitespace is trimmed at load; **duplicates are allowed** (operator's choice).
- `<`, `>`, `&` are auto-escaped at render time, so pasting `<@U123>` into the pool will NOT accidentally ping a Slack user.

Slack status messages use a playful template regardless of whether a nickname is set:

- Preparing: `:toolbox: Alice 正在暖機...`
- Running: `:fire: Alice 開工啦！(claude) · 奮鬥 1m23s`
- Stats: `Alice 已經敲了 15 次工具、翻了 8 份檔`

(Text is Chinese because this is a zh-first product; the template applies to every worker uniformly.)
```

- [ ] **Step 3: Sanity-check markdown renders**

Run: `grep -n nickname_pool docs/configuration-worker*.md`
Expected: each file contains the new block and schema line.

- [ ] **Step 4: Commit**

```bash
git add docs/configuration-worker.md docs/configuration-worker.en.md
git commit -m "docs: document nickname_pool and playful status text"
```

---

## Task 13: Final integration check — build + test everything

A single green-path run to make sure all three modules still compile and no test regressed.

- [ ] **Step 1: Build all modules**

Run from repo root:
```bash
cd app && go build ./... && cd ..
cd worker && go build ./... && cd ..
cd shared && go build ./... && cd ..
go build ./...
```
Expected: no output for any of them.

- [ ] **Step 2: Run all unit tests**

```bash
cd app && go test ./... -short && cd ..
cd worker && go test ./... -short && cd ..
cd shared && go test ./... -short && cd ..
go test ./... -short
```
Expected: PASS everywhere.

- [ ] **Step 3: Run `test/import_direction_test.go`** (landmine — ensures we didn't cross module boundaries)

Run: `go test ./test/ -run TestImportDirection -v`
Expected: PASS — this would catch accidental `worker → app` or `shared → worker` imports.

- [ ] **Step 4: Manual smoke (optional but valuable)**

With a local `worker.yaml` that has `nickname_pool: ["Alice","Bob"]` and `count: 3`:

```bash
./agentdock worker --config /tmp/test-worker.yaml
```

Expected log line: `nickname 池小於 worker 數，部份 worker 將無暱稱 pool_size=2 worker_count=3`. Trigger a `@bot` triage in Slack and verify one of `:toolbox: Alice 正在暖機...` / `:toolbox: Bob 正在暖機...` / `:toolbox: worker-2 正在暖機...` appears.

- [ ] **Step 5: No commit needed** — this task is verification only. If something failed, fix it in the relevant earlier task and re-run from Step 1.

---

## Self-Review Checklist

Before handing off:

**Spec coverage:**
- ✅ `nickname_pool` field added (Task 3) + validated (Task 4)
- ✅ Fisher–Yates picker (Task 1)
- ✅ `Nickname` on `WorkerInfo`, `WorkerNickname` on `StatusReport` (Task 5)
- ✅ Threaded through `statusAccumulator.toReport` (Task 6)
- ✅ Threaded through `pool.Config` → heartbeat Register + executor (Task 7)
- ✅ Wired from `worker.Run` with seeded RNG + pool<count warn log (Task 8)
- ✅ `formatWorkerLabel` + `slackEscape` helpers (Tasks 2, 9)
- ✅ `renderStatusMessage` rewritten (Task 10)
- ✅ Redis round-trip integration test (Task 11)
- ✅ Docs updated (Task 12)
- ✅ Backward compat: `omitempty` JSON tags; `Nicknames nil` in pool.Config tolerated by `nicknameForIndex`
- ✅ Init template: YAML marshal of `nil` slice emits `[]` (verified empirically; no code change needed in `cmd/agentdock/init.go`)

**Placeholder scan:** No TBD/TODO placeholders. Every step contains exact code or exact commands.

**Type consistency:**
- `pickNicknames` lowercase in `nickname.go`, exported wrapper `PickNicknames` used by `worker.go` ✅
- `Nicknames` (capital N, []string) on `pool.Config`, accessed via `p.nicknameForIndex(i)` ✅
- `Nickname` (singular) on `WorkerInfo` and `statusAccumulator`, `WorkerNickname` on `StatusReport` ✅
- `slackEscape`, `formatWorkerLabel` both package-private in `app/bot` ✅
