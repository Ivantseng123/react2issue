# `github-pr-review` Skill — Design

**Date:** 2026-04-21
**Status:** Draft
**Parent spec:** [2026-04-20-workflow-types-design.md](./2026-04-20-workflow-types-design.md) — PR Review workflow

## Problem

The PR Review workflow defined in the parent spec needs a skill that teaches the agent two things:

1. **How to understand the repository it's about to review** — language, framework, test runner, style conventions.
2. **How to post a code review back to GitHub** — line-level comments plus a summary, with the correct REST API payload and without accidentally commenting on lines outside the PR's diff.

The parent spec already sketches this skill as `github-pr-review` and notes it as a separate deliverable. This document is that deliverable's design.

Without a purpose-built skill, the agent's review output is unpredictable: it may hallucinate line numbers, skip style adaptation, or call the wrong REST endpoint (the PR comments API and the PR review API differ in meaningful ways). Line-hallucination is the worst-case failure because it posts user-visible garbage directly to the PR.

## Goals

1. **Fingerprint before reviewing.** The skill always runs a repo fingerprint step (language, framework, style sources) before reading the diff. This grounds the review in the project's actual conventions rather than generic commentary.
2. **Validate before posting.** Every inline comment's target line is checked against the PR's diff hunks before the review is submitted. Comments that don't map to a changed line are dropped, not posted.
3. **Single round-trip review.** The review is posted as one `POST /pulls/{n}/reviews` call (which carries both summary and inline comments), not per-comment. This keeps the PR timeline clean and matches the parent spec's "no retry" contract.
4. **Deterministic orchestration, subjective judgement.** The agent handles review quality (what to flag, how to explain it). Helper scripts handle everything that has a single correct answer (diff parsing, line validation, API payload construction, HTTP transport).
5. **Canonical skill anatomy.** SKILL.md + `scripts/` + optional `references/`, matching the superpowers skill-creator conventions and the existing `triage-issue` precedent.
6. **Pluggable via existing skill loader.** Ships as a baked-in skill at `agents/skills/github-pr-review/`, loaded by `app/skill/loader.go` with no loader changes.

## Non-goals

- **Retry / re-post.** The parent spec forbids automatic retry for PR Review because the agent may have posted partial comments. This skill does not implement retry internally either — a `post-review.sh` error exits non-zero and the agent surfaces the failure.
- **Per-language review reference files.** v1 keeps style adaptation in SKILL.md as a generic checklist with a fallback rule. `references/{go,python,js,rust}.md` split is future work once review rules grow large enough to warrant it.
- **Severity threshold configuration.** v1 hardcodes: any `blocker` comment → `request_changes`; otherwise `comment`; all-clean or only `nit` → `approve`. Config knob deferred.
- **Incremental review.** A second `@bot review <URL>` re-runs the full review; it does not read previous review history and try to do a delta.
- **Enterprise GitHub support.** Inherits parent spec's v1 restriction to `github.com`.
- **Line-level review for vendored / generated / lockfile-only PRs.** The skill explicitly skips detailed review for these and posts summary-only, because line-level comments on 30K-line generated files are noise.
- **The `gh` CLI.** Parent spec requires curl-only because the worker container does not guarantee `gh`. Scripts use `curl` + `jq`.
- **Cross-skill coordination.** This skill runs standalone. The app may ship other skills alongside it (per workflow-types spec), but `github-pr-review` does not call into them.

## Design

### Skill anatomy

```
agents/skills/github-pr-review/
├── SKILL.md                 # entry, <200 lines target
├── scripts/
│   ├── fingerprint.sh       # repo → style/language JSON
│   └── post-review.sh       # review.json → GitHub API (with validation)
└── references/              # reserved; not populated in v1
```

Placement follows the existing `agents/skills/triage-issue/` precedent. `app/skill/loader.go` treats it as a `local` skill via `SkillConfig{Type: "local", Path: "agents/skills/github-pr-review"}`.

### SKILL.md structure

Frontmatter:

```yaml
---
name: github-pr-review
description: Review a GitHub pull request by first fingerprinting the repository's
  language and conventions, then analyzing the diff for correctness, security, and
  style issues, then posting line-level and summary comments back to the PR via the
  GitHub REST API. Use whenever the task asks to review a PR, mentions a GitHub PR
  URL, or asks to audit/check/comment-on changes on a pull request — even if the
  request doesn't name this skill explicitly.
---
```

Body sections (imperative, each step followed by a one-line reason so the agent can extrapolate to edge cases):

1. **Input contract.** Lists exactly what the agent will receive: thread context, PR URL, PR number, PR base ref, repo path (already cloned at PR head), `$GITHUB_TOKEN`. Formatted parallel to the existing `triage-issue` skill.
2. **Fingerprint** — run `scripts/fingerprint.sh > /tmp/fp.json`. Read the JSON. If `style_sources` is empty, note in the review summary that "no project style file found; reviewing against general language conventions."
3. **Diff analysis** — fetch `GET /repos/{owner}/{repo}/pulls/{n}/files` using curl and `$GITHUB_TOKEN`. For each hunk, judge the changes against fingerprint language + style. Produce zero or more comment candidates with `{path, line, side, body, severity}` where severity ∈ `blocker | suggestion | nit`.
4. **Verdict** — decide overall verdict: `approve` only if the diff is clean or only contains `nit`-severity comments; `request_changes` if any `blocker`; otherwise `comment`.
5. **Produce review JSON** — assemble `{summary, verdict, comments[]}` into review.json. The summary is markdown, ≤ 2000 chars, covers the shape of the change and key concerns.
6. **Post** — pipe review.json into `scripts/post-review.sh`. Read its stdout (JSON with `posted`, `skipped`, `skip_reasons`, `review_id`) and its exit code. If exit code is non-zero, do not retry; continue to step 7 with the failure recorded.
7. **Emit result** — output `===REVIEW_RESULT===` followed by JSON per parent spec:
   ```json
   {"summary": "...", "comments_posted": N, "verdict": "approve|comment|request_changes"}
   ```
   If posting failed, include an `error` field. Summary in the emitted JSON is the same summary sent to GitHub (so Slack and GitHub stay consistent).

**Skipping cases** called out explicitly:

- Pure lockfile or generated-file diff (`yarn.lock`, `pnpm-lock.yaml`, `go.sum`, `Cargo.lock`, files under `vendor/` or `third_party/` paths): post summary-only, no inline comments, verdict `comment`, with summary noting why detailed review was skipped.
- PR already merged or closed: review still runs; summary notes that review is historical. (Acceptance of merged/closed PRs is parent-spec behaviour.)

**Writing style guidance baked into the skill:** each rule includes a one-line "why" after the imperative, e.g. "Run fingerprint before reading the diff — generic review commentary on a project with strict style rules is noise." This matches the skill-creator guidance to "explain the why" rather than stack ALWAYS/NEVER.

### `scripts/fingerprint.sh`

**Purpose:** Deterministic repo inspection that returns a small JSON summary, so the agent doesn't rediscover the same facts every run.

**Interface:**

- **Working directory:** assumed cwd is repo root.
- **Args:** none.
- **Stdout:** JSON only.
- **Stderr:** diagnostic log (what files were checked, what matched).
- **Exit code:** 0 always unless shell-level error; missing files are not errors.

**Output schema:**

```json
{
  "language": "go",
  "confidence": "high|medium|low",
  "style_sources": ["CLAUDE.md", ".golangci.yml"],
  "test_runner": "go test",
  "framework": null
}
```

**Probe order:**

1. **Language detection.**
   - Lock / manifest files (highest signal): `go.mod` → go, `package.json` → js (then check for `typescript` in deps → ts), `pyproject.toml` / `setup.py` → python, `Cargo.toml` → rust, `Gemfile` → ruby, `pom.xml` / `build.gradle` → java.
   - Extension voting (tiebreaker): count `*.go`, `*.py`, `*.ts`, `*.js`, `*.rs`, `*.rb`, `*.java`; the largest count that matches the manifest is `high` confidence; mismatch is `medium`; no manifest is `low`.
2. **Style sources** checked in this order (collect all that exist):
   - `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`
   - `.editorconfig`
   - Language-specific: `.golangci.yml`, `.eslintrc.*`, `ruff.toml` / `pyproject.toml` `[tool.ruff]`, `rustfmt.toml`, `rubocop.yml`
3. **Test runner** inferred from language + lockfile scripts (`package.json` `scripts.test`, `Makefile` `test` target, or the language default).
4. **Framework** best-effort: e.g. `react` / `next` / `fastapi` / `django` / `gin` / `echo` from dependency lists. Null if not obvious.

**Language choice: bash + jq.** Reasons: (a) the probes are filesystem reads; bash is adequate; (b) worker container already has `curl` and adding `jq` is a tiny package bump; (c) no Python cold-start cost; (d) matches the deterministic-task expectation from skill-creator for `scripts/`.

### `scripts/post-review.sh`

**Purpose:** Validate every inline comment against the PR's actual diff, then submit the review in one API call.

**Interface:**

- **Stdin:** review.json (schema: `{summary, verdict, comments[{path, line, side, body, severity}]}`).
- **Env required:**
  - `GITHUB_TOKEN` — PAT with PR write scope. Already provided by `EncryptedSecrets` in the worker.
  - `PR_URL` — full PR URL, used to extract owner/repo/number.
  - `PR_NUMBER` — numeric, redundant with `PR_URL` but passed from `WorkflowArgs` for simplicity.
  - `PR_HEAD_SHA` — commit SHA to tie the review to (prevents GitHub's "stale commit" 422 after a force-push).
- **Flags:**
  - `--dry-run` — run validation, print what would be posted, exit without calling POST. For CI and manual testing.

**Workflow:**

1. Read review.json from stdin. Validate JSON shape; exit 2 with stderr explaining the schema error if malformed.
2. `curl GET https://api.github.com/repos/{owner}/{repo}/pulls/{n}/files` with `Authorization: Bearer $GITHUB_TOKEN`.
3. Parse the response into a map `{path → set of valid (line, side) tuples}` by walking each file's patch hunks. A "valid" line is one that appears in the hunk as either a context line, added line, or removed line, with `side` `RIGHT` for context/added and `LEFT` for removed.
4. For each comment in review.json:
   - If `path` not in the map → skip, record reason `file not in diff`.
   - If `(line, side)` not in the path's valid set → skip, record reason `line N/side X not in diff`.
   - Otherwise include in the POST payload.
5. Build the `POST /pulls/{n}/reviews` body:
   ```json
   {
     "commit_id": "<PR_HEAD_SHA>",
     "body": "<summary>",
     "event": "APPROVE|REQUEST_CHANGES|COMMENT",
     "comments": [
       {"path": "...", "line": N, "side": "RIGHT", "body": "..."}
     ]
   }
   ```
   Verdict mapping: `approve` → `APPROVE`; `request_changes` → `REQUEST_CHANGES`; `comment` → `COMMENT`.
6. POST. On 2xx, emit stdout `{"posted": P, "skipped": S, "skip_reasons": [...], "review_id": "..."}` and exit 0 (or 1 if any skip happened). On 4xx/5xx, emit stdout with `error` field and exit 2.

**Exit codes:**

- `0` — all comments posted, no skips.
- `1` — review posted, but some comments were skipped due to diff mismatch. This is still a success path; the agent should surface the skip count in the `===REVIEW_RESULT===` summary.
- `2` — fatal (API error, auth failure, malformed input, rate limit after retries). No review was posted.

**Rate limiting:** one exponential backoff retry (2s, 4s) on 429 or 5xx. Beyond that, exit 2.

**Language choice: bash + jq.** Same rationale as fingerprint. The POST payload is small; jq handles JSON assembly cleanly.

### Integration with the rest of the system

- **Skill registration.** Add one entry to the skills config file (consumed by `app/skill/loader.go`):
  ```yaml
  skills:
    - type: local
      path: agents/skills/github-pr-review
  ```
  No loader code changes. The loader already supports `local` type via `loadBakedInSkills` → `loadSingleBakedIn`.
- **Prompt wiring.** The parent spec's `prompt.pr_review.goal` default already references this skill by name. No prompt builder change needed; the hardcoded default text in `app/workflow/pr_review.go` (per parent spec) carries the reference.
- **WorkflowArgs pass-through.** The parent spec defines `WorkflowArgs = {pr_url, pr_number, pr_base_ref}`. `post-review.sh` also needs the PR head SHA. **Required delta to the parent spec:** add `pr_head_sha` as a fourth WorkflowArgs key, captured during URL validation (the parent spec already fetches `head.sha` from the GitHub API during A-path validation). The parent spec's next edit should include this; this skill's implementation plan assumes `pr_head_sha` is present in WorkflowArgs and exported to the agent's env as `PR_HEAD_SHA`.
- **Token plumbing.** `$GITHUB_TOKEN` is already carried by `EncryptedSecrets` to the worker (per parent spec §43). The agent CLI exposes env vars to the agent process; the skill's scripts read them directly.

### Error handling matrix

| Situation | Skill / script behaviour | Agent's `===REVIEW_RESULT===` |
|-----------|--------------------------|-------------------------------|
| Fingerprint script has no language match at all | Fingerprint returns `language: null, confidence: low`. Skill proceeds with "no fingerprint" fallback — reviews using general code-review principles, summary notes this. | `verdict: comment`, summary explains fallback. |
| `$GITHUB_TOKEN` missing or invalid (401) | `post-review.sh` exits 2. No review posted. | Status `error`, `error: "GitHub auth failed"`, `comments_posted: 0`. |
| Token lacks PR write scope (403) | Same as 401. | Same. |
| `PR_HEAD_SHA` mismatch after a force-push (422 from GitHub) | `post-review.sh` exits 2. | Status `error`, `error: "PR head moved during review; re-trigger @bot review"`, `comments_posted: 0`. |
| Rate-limited (429) | Retry twice with backoff; if still limited, exit 2. | Status `error`, `error: "GitHub rate-limited; retry later"`. |
| Some comments mapped to lines not in the diff | Skipped silently by validator; `post-review.sh` exits 1 with skip count. | `verdict` + `comments_posted: N` (post-skip count), summary mentions "M comments suppressed (lines outside diff)". |
| review.json malformed (missing field, wrong type) | `post-review.sh` exits 2 before calling API. | Status `error`, `error: "internal: review schema invalid"`. This is a bug and should be rare; logs it loudly. |
| Lockfile-only diff | Skill short-circuits to summary-only path (detected in step 2 of the process). | `verdict: comment`, `comments_posted: 0`, summary explains why. |

## Testing

### Skill-level evals (stored under `agents/skills/github-pr-review/evals/`)

Three test cases, chosen to cover the main branching in the skill:

| ID | Scenario | What it exercises |
|----|----------|-------------------|
| 1 | Go repo with `CLAUDE.md` describing import-direction rules, PR violates the rules | Fingerprint picks up CLAUDE.md; review flags the violation with a line-level comment; verdict is `request_changes`. |
| 2 | Python repo with no `CLAUDE.md` / no linter configs, PR adds an untested function | Fingerprint returns `style_sources: []`; skill falls back to general conventions; review suggests adding a test; verdict is `comment`. |
| 3 | `pnpm-lock.yaml`-only diff | Skill short-circuits to summary-only; `comments_posted: 0`; `verdict: comment`. |

**Assertions:**

- Objective (scriptable):
  - `===REVIEW_RESULT===` marker is present exactly once in raw output.
  - The JSON after the marker parses and has `{summary, comments_posted, verdict}`.
  - `comments_posted` is a non-negative integer.
  - `verdict` ∈ `{approve, comment, request_changes}`.
  - For runs that post: every inline comment's `(path, line, side)` appears in the fixture PR's `files` response. Verified by a small checker script against a recorded fixture.
- Subjective (human review in the eval viewer):
  - Does the review reflect the CLAUDE.md rule (case 1)?
  - Does the review read as a senior engineer's comment or as generic boilerplate (case 2)?
  - Does case 3's summary actually explain the skip, not just post "LGTM"?

### Unit tests for scripts

- `scripts/fingerprint.sh`:
  - Fixture repos with `go.mod` + Go files → asserts `language: go`, `style_sources` includes `.golangci.yml` when present.
  - Fixture with `package.json` containing `typescript` in deps → `language: ts`.
  - Empty dir → `language: null, confidence: low`, empty `style_sources`, exit 0.
- `scripts/post-review.sh`:
  - Fixture with mocked GitHub API (`curl` replaced by a stub or `GITHUB_API_BASE` env override pointing at a local fixture server):
    - All comments valid → posts once, exit 0.
    - One comment on a line outside diff → skipped, exit 1, skip_reasons non-empty.
    - Auth failure (401 fixture) → exit 2, no POST made.
    - Stale `PR_HEAD_SHA` (422 fixture) → exit 2.
    - `--dry-run` with valid input → no POST, exit 0, prints intended payload.

### Wiring smoke test

- End-to-end against a staging GitHub repo (a scratch repo under a bot account with a known PR): run the skill via the worker, verify a review is posted, verify the `===REVIEW_RESULT===` JSON matches what Slack would render.

## Open questions / future work

- **Per-language `references/`.** When review rules grow beyond a generic checklist, split into `references/{go,python,js,rust}.md`. SKILL.md will reference only the matching file based on fingerprint language. Guard: keep SKILL.md under 500 lines; the first time a per-language section threatens that, split.
- **Severity threshold config.** Operators may want to tune "what counts as a blocker" per repo (e.g. test-optional repo doesn't want "missing tests" as a blocker). A `review_rules.yaml` consumed by the skill would handle it.
- **Resolving in-thread discussion context.** Slack thread discussion may contain context about *why* a PR was opened (e.g. "this is a stopgap before we refactor"). The skill currently ignores thread context; future work: pass it in and let the skill use it to soften / strengthen suggestions.
- **Rate limit awareness.** If the bot is used in a busy repo, GitHub's secondary rate limits may bite. v1 does one retry; v2 may need a cross-job budget.
- **Dry-run mode for Slack.** A `@bot review <URL> --preview` that runs the skill with `--dry-run` and posts the would-be review to Slack instead of GitHub. Useful for reviewing the bot's output before trusting it on sensitive PRs.
- **Metrics.** `WorkflowCompletionsTotal{workflow="pr_review", ...}` covers the coarse count (parent spec). Skill-level counters (comments posted, comments skipped by validator, fingerprint confidence distribution) are nice-to-have; defer until the workflow has actual traffic.
