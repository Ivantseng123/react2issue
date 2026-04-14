# Goreleaser Binary Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `release-publish.yml` with goreleaser to produce pre-built binaries (linux/darwin × amd64/arm64 + windows/amd64) attached to every GitHub Release, alongside multi-arch Docker images.

**Architecture:** A single `.goreleaser.yaml` drives both binary archives and Docker images from one build. `Dockerfile.release` replaces the old multi-stage `Dockerfile` — it expects a pre-built binary from goreleaser rather than compiling one itself. A new `release.yml` workflow runs goreleaser on release publish. A path-filtered `release-validate.yml` runs `goreleaser --snapshot` on PRs that touch release infra.

**Tech Stack:** goreleaser v2, Docker buildx, GitHub Actions (`goreleaser/goreleaser-action@v6`, `actions/setup-go@v5`, `docker/setup-qemu-action@v3`, `docker/setup-buildx-action@v3`, `docker/login-action@v3`), Go 1.25.

**Spec:** [`docs/superpowers/specs/2026-04-14-goreleaser-binary-release-design.md`](../specs/2026-04-14-goreleaser-binary-release-design.md)

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `cmd/bot/main.go` | Modify | Add `commit` / `date` vars; add `-version` flag for testing; extend startup log |
| `Dockerfile.release` | Create | Runtime image that `COPY`s a pre-built binary (no Go builder stage) |
| `.goreleaser.yaml` | Create | Binary builds, archives, checksums, Docker image definitions, manifest list |
| `.github/workflows/release.yml` | Create | Fires on `release: published`; runs goreleaser end-to-end |
| `.github/workflows/release-validate.yml` | Create | PR gate: runs `goreleaser --snapshot` when release infra files change |
| `README.md` | Modify | Add "External worker 依賴" section |
| `Dockerfile` | Delete | Replaced by `Dockerfile.release` |
| `.github/workflows/release-publish.yml` | Delete | Replaced by `release.yml` |

---

## Task 1: Add version metadata + `-version` flag to main.go

**Files:**
- Modify: `cmd/bot/main.go:26` (add `commit`, `date` vars)
- Modify: `cmd/bot/main.go` (add `-version` flag handling near flag parsing)
- Modify: `cmd/bot/main.go:229` (extend startup log)
- Test: `cmd/bot/version_test.go` (Create)

Purpose: Provide a stable, testable surface for ldflags injection. A `-version` flag makes downstream manual verification trivial and gives us a unit-testable entrypoint.

- [ ] **Step 1: Write the failing test**

Create `cmd/bot/version_test.go`:

```go
package main

import "testing"

func TestVersionDefaults(t *testing.T) {
    // When ldflags are not injected, defaults must be stable strings.
    if version != "dev" {
        t.Errorf("version default = %q, want %q", version, "dev")
    }
    if commit != "unknown" {
        t.Errorf("commit default = %q, want %q", commit, "unknown")
    }
    if date != "unknown" {
        t.Errorf("date default = %q, want %q", date, "unknown")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./cmd/bot/ -run TestVersionDefaults -v
```

Expected: FAIL with `undefined: commit` and `undefined: date`.

- [ ] **Step 3: Add `commit` / `date` vars**

Edit `cmd/bot/main.go` around line 26. Replace:

```go
var version = "dev"
```

with:

```go
var (
    version = "dev"
    commit  = "unknown"
    date    = "unknown"
)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./cmd/bot/ -run TestVersionDefaults -v
```

Expected: PASS.

- [ ] **Step 5: Add `-version` flag**

Find the flag parsing block in `cmd/bot/main.go` (grep for `flag.Parse` or `flag.String`). Add before `flag.Parse()`:

```go
showVersion := flag.Bool("version", false, "Print version and exit")
```

Add immediately after `flag.Parse()`:

```go
if *showVersion {
    fmt.Printf("agentdock %s (commit %s, built %s)\n", version, commit, date)
    return
}
```

Ensure `"fmt"` is in the import block (it almost certainly already is; if not, add it).

- [ ] **Step 6: Extend the startup log**

Locate `cmd/bot/main.go:229` (`slog.Info("starting bot", "version", version)`). Replace with:

```go
slog.Info("starting bot", "version", version, "commit", commit, "date", date)
```

- [ ] **Step 7: Verify ldflags injection works end-to-end**

```bash
go build -ldflags "-X main.version=1.2.3 -X main.commit=abc1234 -X main.date=2026-04-14T00:00:00Z" -o /tmp/bot-test ./cmd/bot
/tmp/bot-test -version
```

Expected output:
```
agentdock 1.2.3 (commit abc1234, built 2026-04-14T00:00:00Z)
```

Clean up: `rm /tmp/bot-test`.

- [ ] **Step 8: Commit**

```bash
git add cmd/bot/main.go cmd/bot/version_test.go
git commit -m "feat(bot): add commit/date version metadata and -version flag"
```

---

## Task 2: Create `Dockerfile.release`

**Files:**
- Create: `Dockerfile.release`

Purpose: Runtime-only image that consumes a pre-built binary (provided by goreleaser via build context). Same runtime deps as the old `Dockerfile` (node, opencode, gh, skills) — minus the Go builder stage. Also fixes the `gh` arm64 URL bug.

- [ ] **Step 1: Create the file**

Create `Dockerfile.release`:

```dockerfile
# Runtime image for AgentDock. Binary is supplied by goreleaser; this Dockerfile
# does NOT compile Go. For local Go builds use `go build` or `./run.sh`.
FROM node:22-alpine

RUN apk add --no-cache git ca-certificates curl

# Agent CLIs
RUN npm install -g @anthropic-ai/claude-code @openai/codex

ARG OPENCODE_VERSION=1.4.3
RUN curl -sL https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-x64-musl.tar.gz | \
    tar xzf - -C /usr/local/bin opencode

ARG GH_VERSION=2.65.0
ARG TARGETARCH
RUN curl -sL https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz | \
    tar xz -C /usr/local/bin --strip-components=2 gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh

# Binary produced by goreleaser (must be present in build context at root)
COPY bot /bot
RUN chmod +x /bot

# Agent skills (goreleaser extra_files copies the directory into context)
COPY agents/skills/ /opt/agents/skills/
RUN mkdir -p /home/node/.claude/skills && \
    for d in /opt/agents/skills/*/; do \
      ln -s "$d" /home/node/.claude/skills/$(basename "$d"); \
    done

RUN mkdir -p /data/repos && chown node:node /data/repos

# Default config sample (goreleaser extra_files)
COPY config.example.yaml /config.yaml

USER node
ENTRYPOINT ["/bot"]
CMD ["-config", "/config.yaml"]
```

- [ ] **Step 2: Smoke-test the Dockerfile with a fake binary**

Goreleaser isn't wired up yet, but we can verify the Dockerfile is syntactically valid and builds given a fake `bot`:

```bash
# Build a throwaway linux/amd64 binary the Dockerfile can consume
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bot ./cmd/bot
docker build -f Dockerfile.release -t agentdock:release-smoke .
docker run --rm agentdock:release-smoke -version
```

Expected: the smoke build completes and `-version` prints `agentdock dev (commit unknown, built unknown)` (ldflags not applied — that's fine here).

Clean up:
```bash
rm bot
docker image rm agentdock:release-smoke
```

- [ ] **Step 3: Commit**

```bash
git add Dockerfile.release
git commit -m "build: add Dockerfile.release consumed by goreleaser"
```

---

## Task 3: Create `.goreleaser.yaml`

**Files:**
- Create: `.goreleaser.yaml`

Purpose: Declarative description of binaries, archives, checksums, Docker images, and manifest lists. Single source of truth for release artifacts.

- [ ] **Step 1: Create the file**

Create `.goreleaser.yaml`:

```yaml
version: 2
project_name: agentdock

builds:
  - id: bot
    main: ./cmd/bot
    binary: bot
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ignore:
      - goos: windows
        goarch: arm64
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}

archives:
  - id: default
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files:
      - README.md
      - LICENSE
      - config.example.yaml

checksum:
  name_template: 'checksums.txt'

changelog:
  disable: true   # release-please owns CHANGELOG.md; goreleaser must not contribute

dockers:
  - image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
    dockerfile: Dockerfile.release
    use: buildx
    goarch: amd64
    build_flag_templates:
      - --platform=linux/amd64
    extra_files:
      - agents/skills
      - config.example.yaml

  - image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'
    dockerfile: Dockerfile.release
    use: buildx
    goarch: arm64
    build_flag_templates:
      - --platform=linux/arm64
    extra_files:
      - agents/skills
      - config.example.yaml

docker_manifests:
  - name_template: 'ghcr.io/ivantseng123/agentdock:{{ .Version }}'
    image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'
  - name_template: 'ghcr.io/ivantseng123/agentdock:{{ .Major }}.{{ .Minor }}'
    image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'

release:
  github:
    owner: Ivantseng123
    name: agentdock
  mode: keep-existing   # append assets only; do NOT modify release body (release-please owns it)
```

Note: version numbers for `OPENCODE_VERSION` / `GH_VERSION` are intentionally **not** passed as `--build-arg` here. The Dockerfile's `ARG` defaults are the single source of truth.

- [ ] **Step 2: Validate config syntax**

Install goreleaser locally if missing (`brew install goreleaser` on macOS). Then:

```bash
goreleaser check
```

Expected: `✓ config is valid`.

- [ ] **Step 3: Run full snapshot dry-build**

```bash
goreleaser release --snapshot --clean --skip=publish
```

Expected:
- Completes without error (takes 2–5 min).
- `dist/` contains 5 archive files (`.tar.gz` for linux/darwin, `.zip` for windows) + `checksums.txt`.
- Two local images exist: `ghcr.io/ivantseng123/agentdock:<snapshot-version>-amd64` and `-arm64`.

Verify:
```bash
ls dist/ | grep -E '\.(tar\.gz|zip|txt)$'
docker images | grep agentdock
```

- [ ] **Step 4: Verify a binary from the snapshot**

```bash
tar xzOf dist/agentdock_*_linux_amd64.tar.gz bot > /tmp/bot-snapshot
chmod +x /tmp/bot-snapshot
/tmp/bot-snapshot -version
```

Expected output (version will be a snapshot placeholder like `0.0.0-next-SNAPSHOT-xxxx`):
```
agentdock 0.0.0-next-... (commit <sha>, built <timestamp>)
```

`commit` and `date` must be populated (not `unknown`). Clean up:
```bash
rm /tmp/bot-snapshot
rm -rf dist/
docker image rm $(docker images 'ghcr.io/ivantseng123/agentdock' -q) 2>/dev/null || true
```

- [ ] **Step 5: Commit**

```bash
git add .goreleaser.yaml
git commit -m "build: add .goreleaser.yaml for binary + multi-arch docker release"
```

---

## Task 4: Create `.github/workflows/release.yml`

**Files:**
- Create: `.github/workflows/release.yml`

Purpose: Run goreleaser end-to-end when a GitHub Release is published (by release-please) or manually re-run via `workflow_dispatch`.

- [ ] **Step 1: Create the file**

Create `.github/workflows/release.yml`:

```yaml
name: Release
on:
  release:
    types: [published]
  workflow_dispatch:
    inputs:
      tag:
        description: 'Release tag to build (e.g. v1.2.3)'
        required: true

permissions:
  contents: write   # goreleaser append assets to existing release
  packages: write   # ghcr.io

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          ref: ${{ inputs.tag || github.event.release.tag_name }}

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 2: Lint the workflow**

```bash
# If actionlint is installed:
actionlint .github/workflows/release.yml
# Otherwise skip — CI will surface syntax errors.
```

Expected: no errors, or actionlint not installed (acceptable).

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add release workflow that runs goreleaser on tag publish"
```

---

## Task 5: Create `.github/workflows/release-validate.yml`

**Files:**
- Create: `.github/workflows/release-validate.yml`

Purpose: PR gate. Runs `goreleaser --snapshot` only when release infra files change — catches broken `.goreleaser.yaml` / `Dockerfile.release` before tag time.

- [ ] **Step 1: Create the file**

Create `.github/workflows/release-validate.yml`:

```yaml
name: Release Validate
on:
  pull_request:
    paths:
      - '.goreleaser.yaml'
      - 'Dockerfile.release'
      - '.github/workflows/release.yml'
      - '.github/workflows/release-validate.yml'

permissions:
  contents: read

jobs:
  snapshot:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3

      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --snapshot --clean --skip=publish
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/release-validate.yml
git commit -m "ci: add PR-time goreleaser snapshot validation for release infra changes"
```

---

## Task 6: Update README.md with External Worker dependency section

**Files:**
- Modify: `README.md`

Purpose: Disclose that downloaded binaries are not self-contained. Worker mode `exec`s `claude`/`codex`/`opencode`/`gemini` + `gh` + `git`; users must install these themselves. Windows platform gets a caveat.

- [ ] **Step 1: Locate the External Worker row in the deployment table**

Open `README.md` and find the row:

```
| External Worker | Redis + runner binary | 未來擴展：外部機器跑 `bot worker`，連同一個 Redis |
```

(Around line 64, per recent grep.)

- [ ] **Step 2: Add a new subsection after the Redis 模式架構 section**

Find the end of the `#### Redis 模式架構` section (before `## 觸發方式`). Add:

```markdown
#### External Worker 依賴

如果你下載 GitHub Release 附的 binary 在外部機器跑 `bot worker`，**binary 不是 self-contained**。Worker 會 `exec` 以下 CLI，請先自行安裝並確認在 `PATH` 中：

- **至少一個 agent CLI**（config 裡選定的那個）：
  - `@anthropic-ai/claude-code`（npm）
  - `@openai/codex`（npm）
  - `opencode`（見 [anomalyco/opencode](https://github.com/anomalyco/opencode) releases）
  - `gemini`（如有使用）
- **`gh` CLI**（建立 GitHub issue 用）
- **`git`**（clone repo）

若不想自行管理這些依賴，改用 Docker image：`ghcr.io/ivantseng123/agentdock:<version>` 已預裝全部 runtime。

**Windows 備註**：上述 CLI 的 Windows 原生支援由上游廠商提供，若遇相容性問題建議改用 Docker image（需 WSL2 或 Linux VM）。
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add external worker binary dependency section"
```

---

## Task 7: Delete old `Dockerfile` and `release-publish.yml`

**Files:**
- Delete: `Dockerfile`
- Delete: `.github/workflows/release-publish.yml`

Purpose: Single source of truth. No stale files to bit-rot.

- [ ] **Step 1: Verify no active references**

```bash
grep -rn "Dockerfile" --include='*.sh' --include='*.yaml' --include='*.yml' --include='*.go' --include='*.md' . \
  | grep -v 'Dockerfile.release' \
  | grep -v 'docs/ideas/' \
  | grep -v 'docs/superpowers/' \
  | grep -v 'CHANGELOG.md'
```

Expected: no output (or only historical references already reviewed).

```bash
grep -rn "release-publish" --include='*.sh' --include='*.yaml' --include='*.yml' --include='*.go' --include='*.md' .
```

Expected: no output.

- [ ] **Step 2: Delete the files**

```bash
git rm Dockerfile .github/workflows/release-publish.yml
```

- [ ] **Step 3: Verify build still works**

```bash
./run.sh --help 2>/dev/null || go build -o /tmp/bot-test ./cmd/bot && rm /tmp/bot-test
```

Expected: `go build` succeeds (run.sh may try to start the bot; ignore anything after build succeeds).

- [ ] **Step 4: Commit**

```bash
git commit -m "chore: remove old Dockerfile and release-publish.yml (replaced by goreleaser)"
```

---

## Task 8: Final end-to-end local snapshot + test suite

**Files:** None modified; verification only.

Purpose: Confirm the whole branch works before opening the PR. This is the Q4-decision-D "local gate" for the transition.

- [ ] **Step 1: Run full Go test suite**

```bash
go test ./...
```

Expected: all tests pass (currently 69 + the new `TestVersionDefaults` = 70).

- [ ] **Step 2: Run goreleaser snapshot end-to-end**

```bash
goreleaser release --snapshot --clean --skip=publish
```

Expected: 5 archives + checksums in `dist/`; two local Docker images built; no errors.

- [ ] **Step 3: Spot-check one archive per OS**

```bash
tar tzf dist/agentdock_*_linux_arm64.tar.gz
tar tzf dist/agentdock_*_darwin_arm64.tar.gz
unzip -l dist/agentdock_*_windows_amd64.zip
```

Each must contain: `bot` (or `bot.exe` for windows), `README.md`, `LICENSE`, `config.example.yaml`.

- [ ] **Step 4: Spot-check both Docker images**

```bash
# amd64
docker run --rm --platform=linux/amd64 \
  $(docker images 'ghcr.io/ivantseng123/agentdock' --format '{{.Repository}}:{{.Tag}}' | grep amd64 | head -1) \
  -version

# arm64 (needs qemu; macOS/Apple Silicon already has it)
docker run --rm --platform=linux/arm64 \
  $(docker images 'ghcr.io/ivantseng123/agentdock' --format '{{.Repository}}:{{.Tag}}' | grep arm64 | head -1) \
  -version
```

Both must print `agentdock <snapshot-version> (commit <sha>, built <date>)`.

- [ ] **Step 5: Verify arm64 image has arm64 `gh` (TARGETARCH plumbing check)**

Alpine-based images don't ship `file`. Copy `gh` out and inspect on the host:

```bash
ARM64_IMG=$(docker images 'ghcr.io/ivantseng123/agentdock' --format '{{.Repository}}:{{.Tag}}' | grep arm64 | head -1)
CID=$(docker create "$ARM64_IMG")
docker cp "$CID:/usr/local/bin/gh" /tmp/gh-arm64-check
docker rm "$CID" > /dev/null
file /tmp/gh-arm64-check
rm /tmp/gh-arm64-check
```

Expected: output contains `aarch64` or `ARM aarch64`. If it contains `x86-64`, the `TARGETARCH` plumbing is broken — go back to Task 2.

- [ ] **Step 6: Clean up and push**

```bash
rm -rf dist/
docker image prune -f

git push -u origin feat/goreleaser-binary-release
```

- [ ] **Step 7: Open PR**

```bash
gh pr create --fill --base main
```

Include a reference to issue #9 in the body. The PR will trigger `release-validate.yml` — verify the snapshot job goes green before merging.

---

## Post-merge checklist (for first real release)

After the feature PR merges and the next release-please Release PR is merged (producing a real tag):

- [ ] `release.yml` workflow completes green
- [ ] GitHub Release page shows 5 archives + `checksums.txt`
- [ ] `docker buildx imagetools inspect ghcr.io/ivantseng123/agentdock:<version>` shows both `linux/amd64` and `linux/arm64`
- [ ] `gh release download <tag> -p '*linux_amd64*'` → extract → `./bot -version` prints correct version/commit/date
- [ ] `docker run --rm ghcr.io/ivantseng123/agentdock:<version> -version` matches

If any step fails: re-run `release.yml` via `workflow_dispatch` with the tag. goreleaser `--clean` + `release.mode: keep-existing` make this idempotent.
