# Design: Homebrew Tap Release Channel

**Date:** 2026-04-16
**Related:**
- Issue [#29](https://github.com/Ivantseng123/agentdock/issues/29)(本 spec 的目標 issue)
- Issue [#9](https://github.com/Ivantseng123/agentdock/issues/9) / [`2026-04-14-goreleaser-binary-release-design.md`](2026-04-14-goreleaser-binary-release-design.md)(前置工作:goreleaser release pipeline 已就位)

## Problem Statement

**HMW**:讓團隊 macOS / Linux 開發者透過 `brew install agentdock` 取得 binary,取代目前的 `git clone && go build` 流程。最小到 `agentdock --version` 能跑即交差。

目前 release 透過 release-please → goreleaser 產出 tar.gz / zip(linux/darwin/windows × amd64/arm64)與 Docker 映像(`ghcr.io/ivantseng123/agentdock`)。無 Homebrew 管道。

## Goals

- 新增 Homebrew 發佈管道,每次 release 自動更新 `Ivantseng123/homebrew-tap` 的 `Formula/agentdock.rb`
- 同時支援 macOS(amd64 / arm64)與 Linux(amd64 / arm64,走 linuxbrew)
- 發佈過程採 PR-then-auto-merge,留 audit trail 並於 merge 前跑 `brew audit --strict --online`
- 既有 tar.gz / zip / Docker pipeline 完全不動

## Non-Goals

- 申請 homebrew-core(內部使用,不需社群 review)
- `brew services` / launchd plist(正式部署走 Docker/K8s)
- Homebrew Cask(binary 用 Formula 就對了)
- 改 `go.mod` module path(副作用太大)
- Formula 打包 `agents/skills`(`--version` 不需;需完整資源請走 Docker 映像)
- Formula `caveats` 區塊(第一位踩到 config 痛點的人再補)

## Positioning

Brew 只是 **macOS/Linux 開發輔助**,**不是**正式部署通道。正式部署仍走 Docker/K8s(`ghcr.io/ivantseng123/agentdock`)。此設計不追求取代 Docker pipeline,僅取代「手動 `go build`」。

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Ivantseng123/agentdock                                           │
│                                                                   │
│  release-please PR merge → tag vX.Y.Z → release.yml(既有)        │
│  └─ goreleaser:                                                   │
│       ├─ builds / archives / dockers(既有,不動)                 │
│       └─ brews: 區塊(NEW)                                       │
│            ├─ 產 Formula/agentdock.rb                            │
│            ├─ push 到 bump-agentdock-vX.Y.Z branch(於 tap repo) │
│            └─ 開 PR → tap main                                    │
│                                                                   │
│  使用 HOMEBREW_TAP_TOKEN(Ivantseng123 名下 fine-grained PAT)   │
└──────────────────────────────────────────────────────────────────┘
                                │
                                ▼  PR
┌──────────────────────────────────────────────────────────────────┐
│  Ivantseng123/homebrew-tap                                        │
│                                                                   │
│  .github/workflows/auto-merge.yml(NEW)                           │
│    on: pull_request_target                                       │
│    if: actor == 'Ivantseng123'                                   │
│        AND head.ref startsWith 'bump-'                           │
│    steps:                                                         │
│      1. checkout PR head(只讀 formula 檔)                       │
│      2. brew audit --strict --online Formula/agentdock.rb        │
│      3. gh pr merge --squash                                      │
│                                                                   │
│  .github/CODEOWNERS(NEW)                                         │
│    /.github/ @Ivantseng123                                       │
│                                                                   │
│  Branch protection on main(手動 UI 設定):                       │
│    ✓ Require PR before merge                                     │
│    ✓ Require review from Code Owners                             │
│    ✓ Include administrators                                      │
│    ✗ Require approvals(避免卡住 auto-merge)                     │
│    ✗ Require status checks(避免 self-referential 循環)          │
└──────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌──────────────────────────────────────────────────────────────────┐
│  Team 開發者(macOS / Linux)                                     │
│                                                                   │
│  一次性:                                                         │
│    brew tap Ivantseng123/tap                                     │
│    brew install agentdock                                        │
│                                                                   │
│  之後:brew upgrade agentdock                                     │
└──────────────────────────────────────────────────────────────────┘
```

**Trust 邊界**:

- 誰能推可信 PR = 誰持有 `HOMEBREW_TAP_TOKEN`。
- PAT owner 是 `Ivantseng123`(tap owner account)。
- PR 被 auto-merge 必須同時滿足:
  1. `pull_request.user.login == 'Ivantseng123'`
  2. `pull_request.head.ref` 以 `bump-` 開頭
  3. `brew audit --strict --online` 通過
- 未來開放新 source repo 推 tap → 將 PAT 存進該 repo 的 secrets,tap 側零配置變更。

## Components

### A. agentdock 側改動

#### A1. `.goreleaser.yaml` — 新增 `brews:` 區塊

位置:`docker_manifests:` 之後、`release:` 之前。

```yaml
brews:
  - name: agentdock
    repository:
      owner: Ivantseng123
      name: homebrew-tap
      branch: main
      token: '{{ .Env.HOMEBREW_TAP_TOKEN }}'
      pull_request:
        enabled: true
        branch: "bump-agentdock-{{.Tag}}"
    directory: Formula
    commit_author:
      name: Ivantseng123
      email: 170440613+Ivantseng123@users.noreply.github.com
    commit_msg_template: 'chore: bump agentdock to {{ .Tag }}'
    description: AgentDock — Slack-driven LLM agent orchestrator
    homepage: https://github.com/Ivantseng123/agentdock
    license: MIT
    install: |
      bin.install "agentdock"
    test: |
      system "#{bin}/agentdock", "--version"
    skip_upload: auto
```

設計要點:
- `pull_request.branch` 顯式指定 `bump-agentdock-{{.Tag}}`,與 tap auto-merge workflow 的 `startsWith(head.ref, 'bump-')` 條件對齊
- `commit_author.email` 使用 GitHub noreply 格式,數字 170440613 為 `Ivantseng123` 的 user ID(`gh api users/Ivantseng123 --jq '.id'` 已驗)
- `skip_upload: auto` 於 tag 帶 prerelease 標記(如 `-rc1`)時跳過 brew 發佈
- `install` 顯式寫一行可讀;`test` 依 issue 要求最小化

#### A2. `.github/workflows/release.yml` — 新增 env var

現有 goreleaser step 的 `env:` 下加一行:

```yaml
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}   # NEW
```

`release-please.yml` 以 `secrets: inherit` 呼叫 `release.yml`,無需在 `release-please.yml` 額外宣告 secret。

#### A3. `.goreleaser.yaml` — archive 加入 LICENSE

既有 archive `files:` 追加一行(使 `brew audit` 能在 archive 內找到 LICENSE):

```yaml
archives:
  - id: default
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files:
      - README.md
      - README.en.md
      - LICENSE          # NEW
      - docs/MIGRATION-v1.md
```

#### A4. 新增 `LICENSE` 檔案(repo 根目錄)

內容採標準 MIT template:

```
MIT License

Copyright (c) 2025-2026 Ivantseng123

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

此檔完整符合 SPDX MIT 模板,版權行填為 repo owner account,年份涵蓋 2025 首次 commit 到 2026 當年度。

#### A5. `README.md` / `README.en.md` — 新增 install 區塊

於 Installation 相關章節(若無則新增)加入:

```markdown
## Install via Homebrew (macOS / Linux)

brew tap Ivantseng123/tap
brew install agentdock
agentdock --version

# Upgrade:
brew upgrade agentdock
```

並標註:「brew 安裝僅提供 binary;`app`/`worker` 子指令需額外配置 config 與外部 CLI(claude、opencode、codex、gemini)。正式部署請使用 Docker 映像 `ghcr.io/ivantseng123/agentdock`。」

### B. homebrew-tap 側改動

#### B1. `.github/workflows/auto-merge.yml`(新增)

```yaml
name: Auto-merge trusted formula bumps

on:
  pull_request_target:
    types: [opened, synchronize, reopened]

permissions:
  contents: write
  pull-requests: write

concurrency:
  group: auto-merge-${{ github.event.pull_request.number }}
  cancel-in-progress: true

jobs:
  auto-merge:
    runs-on: ubuntu-latest
    if: >-
      github.event.pull_request.user.login == 'Ivantseng123'
      && startsWith(github.event.pull_request.head.ref, 'bump-')
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}
      - uses: Homebrew/actions/setup-homebrew@master
      - name: Audit changed formulas
        run: |
          changed=$(gh pr view ${{ github.event.pull_request.number }} \
            --json files --jq '.files[].path' \
            | grep -E '^(Formula|Casks)/' || true)
          if [ -z "$changed" ]; then
            echo "No formula or cask changes; skipping audit"
            exit 0
          fi
          for f in $changed; do
            echo "::group::Auditing $f"
            brew audit --strict --online "$f"
            echo "::endgroup::"
          done
        env:
          GH_TOKEN: ${{ github.token }}
      - name: Merge
        run: gh pr merge ${{ github.event.pull_request.number }} --squash
        env:
          GH_TOKEN: ${{ github.token }}
```

設計要點:
- `pull_request_target`(非 `pull_request`)確保 workflow 從 base branch 執行,PR 改不動自己的 trust 邏輯
- `if:` 兩條件 AND,過濾「非 Ivantseng123」與「非 `bump-` prefix」的 PR(人手動開的 review PR 不會被吞)
- 無匹配 formula 檔時 clean exit,不阻擋
- Fail-open 策略:任何步驟失敗均 leave PR open,workflow 紅臉通知你即可
- 使用 `GITHUB_TOKEN`(自動配發)不需任何外掛 PAT 儲存於 tap

#### B2. `.github/CODEOWNERS`(新增)

```
/.github/ @Ivantseng123
```

僅保護 workflow 目錄。Formula/Casks 路徑不列,以免 auto-merge 被 CODEOWNER review 阻擋。

#### B3. Branch protection on `main`(手動 UI 設定)

Settings → Branches → Add rule for `main`:

| 設定 | 值 | 說明 |
|---|---|---|
| Require a pull request before merging | ✅ | 禁止 direct push |
| Require approvals | ❌ | 若要求 approval,auto-merge 會無限等 |
| Require review from Code Owners | ✅ | 僅對 CODEOWNER-pathed 檔案觸發(`.github/` 下) |
| Dismiss stale PR approvals when new commits pushed | ✅ | 防止舊 approval 被污染 |
| Require status checks to pass before merging | ❌ | 避免 auto-merge workflow 自己是 check 的 self-referential 死結 |
| Include administrators | ✅ | 紀律;你自己也得走 PR |
| Allow force pushes | ❌ | |
| Allow deletions | ❌ | |

**效果矩陣:**

| PR 類型 | 路徑 | CODEOWNER gate | 誰 merge |
|---|---|---|---|
| goreleaser 的 formula bump | `Formula/agentdock.rb` | 不觸發 | auto-merge workflow |
| 手動 formula 修改 | `Formula/*.rb` 或 `Casks/*.rb`(branch 非 `bump-*`) | 不觸發 | 你自己手動 merge |
| Trust / workflow 變更 | `.github/**` | 觸發 → 需 Ivantseng123 approve | 只有 Ivantseng123 能 approve |

## Order of Operations

執行順序不可隨意調整,否則 release pipeline 可能半癱:

1. **產出 PAT**(Ivantseng123 帳號):
   - GitHub Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token
   - Resource owner: `Ivantseng123`
   - Repository access: Only select repositories → `Ivantseng123/homebrew-tap`
   - Permissions:`Contents: Read and write`、`Pull requests: Read and write`、`Metadata: Read-only`(自動勾)
   - Expiration: 1 年
   - 產出後暫存,別急著貼進 secrets

2. **Tap repo PR #1**:新增 `.github/workflows/auto-merge.yml` + `.github/CODEOWNERS`
   - 此時 tap 還沒 branch protection,可 self-merge 過渡
   - Merge 後 workflow 就位

3. **啟用 tap branch protection**(依 B3 勾選)
   - 從此 tap 所有更動都必須走 PR

4. **PAT 存進 agentdock secrets**:
   - `Ivantseng123/agentdock` → Settings → Secrets and variables → Actions → New repository secret
   - Name: `HOMEBREW_TAP_TOKEN`
   - Value: step 1 產出的 PAT

5. **agentdock PR**(feat commit type,將觸發下一次 release-please):
   - 新增 `LICENSE`
   - 改 `.goreleaser.yaml`(brews: 區塊 + archive files 加 LICENSE)
   - 改 `.github/workflows/release.yml`(env 多 HOMEBREW_TAP_TOKEN)
   - 改 `README.md` / `README.en.md`(install 區塊)
   - `release-validate.yml` 會於此 PR 自動跑 `goreleaser --snapshot --skip=publish`,驗 `.goreleaser.yaml` 語法
   - Review + merge

6. **等下一次 release-please 正式 release**:
   - release-please 產新 release PR → 合併後 tag vX.Y.Z → `release.yml` 自動觸發
   - goreleaser 跑完 → 於 tap 開 PR → auto-merge workflow 跑 → merge 到 tap main

**為何此順序關鍵:**
- step 2 在 3 前:若先 enable protection 再開 PR 裝 workflow,會被 protection 擋住無法 merge
- step 3 在 4 前:protection 就位才釋出 PAT,避免 token 就緒但 tap 沒配套的真空期
- step 4 在 5 前:secret 必須已存在於 agentdock,否則 merge step 5 後觸發 release 會 fail(找不到 env var)

## Verification Plan

### 預驗證(merge step 5 前)

- `release-validate.yml` 自動跑 goreleaser snapshot,驗 `.goreleaser.yaml` 語法無誤
- 本地人工 review goreleaser 新區塊符合 v2 schema
- 本地 `brew search agentdock` 確認無名稱衝突(✅ 已驗)

### E2E 驗證路徑(推薦:路徑 2,下次正式 release 即地測)

**選擇理由**:`skip_upload: auto` 會讓 prerelease tag 跳過 brew,因此無法用 `-rc1` 手動驗。fork sandbox 成本過高。最低成本路徑是讓下一次真實 release-please release 當 canary;失敗 blast radius 僅影響 brew 通道(Docker / tar.gz 已 publish 於更早 pipeline 步驟),可 revert `brews:` 區塊後重 release 救援。

### 成功判定 checklist

- [ ] Release workflow run 完無錯
- [ ] Tap repo 收到 `bump-agentdock-vX.Y.Z` PR,commit author `Ivantseng123`
- [ ] Auto-merge workflow 通過 `if:` 條件,`brew audit --strict --online` pass
- [ ] PR 自動 squash-merge 進 tap main
- [ ] 測試機執行 `brew tap Ivantseng123/tap && brew install agentdock && agentdock --version`
- [ ] 輸出含 `vX.Y.Z`、`commit <hash>`、`built <date>` 三欄
- [ ] `brew info agentdock` 顯示正確 description / homepage / license

## Implementation-time Unknowns(高風險,實作時第一次跑必須緊盯)

| 假設 | 為何不確定 | 如何驗證 |
|---|---|---|
| goreleaser v2 `brews.repository.pull_request.enabled: true` schema 如上範例 | v2 docs 的 brews 頁面未權威抓到,Casks 頁面確認該 schema 對 casks 適用,brews 應同構但需實測 | goreleaser step 若 config parse 失敗會在 step 5 之前的 `release-validate.yml` 暴露 |
| `pull_request.branch` 欄位接受 goreleaser template `{{.Tag}}` | 多數 goreleaser 欄位支援 template,但此欄位文件未明確列出 | 首次 release 看 tap PR head ref 是否為 `bump-agentdock-vX.Y.Z` |
| `release-validate.yml` 以 `--snapshot --skip=publish` 跑時不會 eager-resolve `{{ .Env.HOMEBREW_TAP_TOKEN }}` 而失敗 | goreleaser v2 template 應為 lazy 評估,`--skip=publish` 跳過 brews step 即不觸發 | 觀察 release-validate 於 step 5 PR 是否 pass |
| `Homebrew/actions/setup-homebrew@master` 於 `pull_request_target` context 執行正常 | `pull_request_target` 使用頻率低於 `pull_request` | 首次 tap PR 進來看 workflow log |
| `brew audit --strict --online` 對 goreleaser 預設產出的 Formula 直接過 | formula template 由 goreleaser 產,欄位格式細節可能挑刺(description 大小寫、license 格式等) | 首次 audit 失敗訊息為主要診斷來源 |
| Branch protection `Include administrators` + `Require review from Code Owners` 不阻擋 GITHUB_TOKEN 在 Formula 路徑上的 auto-merge | GitHub branch protection 對 Apps 的豁免行為偶有變動 | 首次 tap PR merge 步驟若卡住即為此原因 |

**策略:上述任一破掉皆非 blocker**,設計已保留調整空間(`.goreleaser.yaml` 欄位調整、workflow `if:` 微調、protection 臨時關 `Include administrators`)。

## Known Residual Risks

- **PAT 外洩**:fine-grained PAT 若外洩,攻擊者可以 `Ivantseng123` 身份開 PR 到 tap,branch 命為 `bump-*`,auto-merge 會過 `actor + branch_pattern` 檢查。最後一道防線是 `brew audit --strict --online`,對精心偽造的 formula 仍可能被繞過。
  - **接受理由**:此 threat model 需要 PAT 外洩 + 攻擊者有時間 craft 能過 audit 的 formula,頻率低。既有替代強化(OIDC workflow_run 出處驗證)在 Q4 grill 時被取捨掉,不在本輪 scope。
  - **緩解**:PAT 最長 1 年過期,strong rotation discipline。
- **PAT 過期忘了 rotate**:release 會紅,手動更新 secret 後 `gh workflow run release.yml` 重跑即可。
  - **緩解**:將 rotation 日期寫進個人 calendar + GitHub 到期前自動 email 提醒。
- **Async coupling**:agentdock release.yml 於 goreleaser 打開 tap PR 那刻即 success;tap auto-merge 若 audit 失敗,release page 顯示 vX.Y.Z 已發但 brew 通道尚未更新。用戶 `brew upgrade` 仍為舊版。
  - **緩解**:GitHub 預設會對 workflow 失敗寄 email 通知 repo owner。接到即手動救援。
- **pinsnap 手動 bump 改走 PR**:branch protection 啟用後,未來 pinsnap cask 更新需開 PR。Commits 作者為 `Ivantseng123`、branch 非 `bump-*` → auto-merge workflow 的 `if:` 不過 → 需手動 merge。
  - **非 bug,是流程改變**。未來若要 pinsnap 也 auto-merge,改其 bump 流程讓 branch 命為 `bump-pinsnap-*` 即可自動涵蓋。

## Prerequisites(清單)

**Ivantseng123 帳號手動操作:**

- [ ] 登入 GitHub,Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token,依 Order of Operations step 1 設定
- [ ] 產出的 PAT 字串暫存於密碼管理器

**agentdock repo 設定:**

- [ ] Settings → Secrets and variables → Actions → New repository secret
  - Name: `HOMEBREW_TAP_TOKEN`
  - Value: 上述 PAT(於 step 4 執行)

**homebrew-tap repo 設定:**

- [ ] Settings → Branches → Add branch protection rule(於 step 3 執行,依 B3 勾選)

## Post-Launch Onboarding

完成首次成功 release 後,在 team 溝通渠道(Slack #dev 頻道或相應)廣播:

```
AgentDock 現已支援 Homebrew 安裝(macOS / Linux):

brew tap Ivantseng123/tap
brew install agentdock
agentdock --version

注意:brew 只裝 binary,`app`/`worker` 子指令仍需配置 config 與外部 CLI。
正式部署請繼續使用 Docker 映像 ghcr.io/ivantseng123/agentdock。
```

此溝通為 scope 內附帶項目,不另開 issue。
