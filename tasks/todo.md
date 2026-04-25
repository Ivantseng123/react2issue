# Todo: Workflow Output Boundary — Ask Raw Fallback

Plan: `tasks/plan.md`
Spec: `docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md`

## Task 1 — Parser fallback (`app/workflow/ask_parser.go`, `_test.go`) — complete

- [x] Add `ResultSource string` field to `AskResult` (`json:"-"`)
- [x] Schema-path success sets `ResultSource = "schema"`
- [x] Marker-not-found path runs syntactic check, returns `"raw_fallback"` on pass
- [x] Syntactic check: non-empty after `TrimSpace`, meets min-length (10 runes)
- [x] Test: schema path → `"schema"`
- [x] Test: missing-marker + plain text → `"raw_fallback"`
- [x] Test: missing-marker + empty / whitespace / short stdout → error
- [x] Test: marker present + malformed JSON → unchanged error
- [x] `go test ./app/workflow -run TestParseAskOutput -v` green
- [x] `go vet ./...` clean
- [x] regression fix: redact_log_test ask cases now drive parse-fail via marker-present + malformed JSON

## Task 2 — Handler banner + metric (`app/workflow/ask.go`, `_test.go`) — complete

- [x] Branch on `parsed.ResultSource` in `HandleResult`
- [x] Prepend `:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式\n\n` on fallback path
- [x] Increment `WorkflowCompletionsTotal{status="fallback_raw"}` on fallback path
- [x] Banner prepended before `askMaxChars` truncation (source ordering verified)
- [x] Test: schema path → no banner, asserted by `TestAskWorkflow_HandleResult_SchemaPathHasNoBanner`
- [x] Test: fallback path → banner present + `fallback_raw` metric increment
- [x] `go test ./app/workflow -run TestAskWorkflow_HandleResult -v` green
- [x] `go build ./...` clean
- [x] `go mod tidy` pulled `prometheus/client_golang/prometheus/testutil` into app module

## Task 3 — End-to-end verification — complete

- [x] `go test ./...` green (app + root modules)
- [x] `go test ./test/...` green (import direction)
- [x] `go build ./cmd/agentdock` succeeds
- [x] Manual sanity covered by `TestAskWorkflow_HandleResult_FallbackPrependsBannerAndIncMetric` exercising the synthesised missing-marker case end-to-end through `HandleResult`

## Checkpoints

- [ ] After Task 1: confirm threshold value (10 runes) and `ResultSource` constant naming (`ResultSourceSchema` / `ResultSourceRawFallback`) — awaiting human review
- [ ] After Task 2: confirm banner wording (`:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式`) with stakeholders — awaiting human review
- [ ] After Task 3: open PR — awaiting human go-ahead
