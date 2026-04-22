---
date: 2026-04-22
status: approved
owners: Ivantseng123
---

# Ask Assistant — Local Skill for `@bot ask`

## Problem

`@bot ask` 目前靠 workflow goal prompt + output_rules 以外，沒有任何行為規範：

- Agent 拿到 thread + optional repo 之後「自由發揮」——想挖 code 就挖、想改 code 也沒人擋、想開 issue 也沒人擋、想偏題回答閒聊也 OK。
- Issue 跟 PR Review 有各自的 skill（`triage-issue`、`github-pr-review`）做為「行為護城河」：告訴 agent 用什麼流程、做什麼 / 不做什麼、輸出長什麼樣。Ask 少了這層，結果就是 Ask 的回答品質跟直接去 claude.ai 問差不多——**用這個 bot 就沒意義了**。
- 使用者陳述：「純 thread 問問題自由發揮，就不需要這個 bot 了。Skill 應該某部分限制範圍邊界。」

結論：Ask 需要一個 local skill 把「邊界」跟「SOP」釘死，讓 Ask 變成一個「範圍明確、拒絕越權、會指路」的對話助手。

## Root Reasoning

- **Ask 的產品定位是「結構化答題」，不是「萬能助手」**：skill 是結構的承載媒介。
- **Skill 形式已經是既有模式**：`agents/skills/<name>/SKILL.md` + `skills.yaml` 註冊；app 的 `submitJob` 把所有 skills mount 給 worker；agent 靠 skill 的 `description` 自動挑。Ask skill 只是再加一個——零新 infra。
- **Ask workflow 程式碼幾乎不需改**：`submitJob` 已經把所有 loaded skills 塞進 job（`ask.go` 裡 `Skills: nil` 的那行其實是 dead code，被 `app.go:286` `job.Skills = loadSkills(...)` 覆蓋）。改動集中在新增 skill 檔 + skill handshake 句子。

## Goals

- 新增 local skill `ask-assistant`，位置 `agents/skills/ask-assistant/SKILL.md`。
- 把使用者確認的 **動作邊界** 跟 **主題邊界** 寫進 skill body。
- 註冊到 `skills.yaml`。
- `ask-assistant` 的 `description` 明確到 Ask workflow 下 agent 會優先挑它（不會誤用 `triage-issue` / `github-pr-review`）。
- Ask 的 `defaultAskGoal` 加一句明確 handshake：`Follow the ask-assistant skill for scope, boundaries, and punt rules.`——跟 Issue / PR Review goal 慣例對齊。
- 把 `ask.go` BuildJob 裡誤導的 `Skills: nil` 註解 / 指派刪掉（dead code，讓檔案更誠實）。

## Non-Goals

- **不改 Ask workflow 的流程結構**：phase、selector、modal、output_rules 都不動。Skill 是行為指引，不是流程改造。
- **不改其他 skills**：`triage-issue` / `github-pr-review` / `mantis` 維持現狀。
- **不為 Ask 開 scripts/ 子資料夾**：Ask 的邊界都是政策性規則，不需執行邏輯；未來若需要分類判斷類腳本再補。
- **不硬性禁止偏題問題的回答**：使用者確認策略是「儘量回答 + 附一句建議改用其他 workflow」，只有純偏題（閒聊/翻譯/代寫）才直接婉拒。
- **不做 output format 邊界**：已經由 workflow 的 output_rules 處理（Slack mrkdwn、≤30000 chars、ASK_RESULT JSON）。
- **不做 runtime / sandbox 級邊界防禦**：Ask agent 照舊帶 `GH_TOKEN`，邊界靠 skill prompt 守，不砍 token 不加 command denylist。公司情境 Ask 多數需要讀 repo（`gh pr view` / `gh search` 之類），砍權限反而沒功能。誤寫靠事後 audit 修 skill 文案。
- **不做 skill load hot-add**：`bakedIn` 只在 `NewLoader` 啟動時掃一次 baked-in 目錄，新增 local skill 必須**重啟 app**；watcher 只對 `skills.yaml` 本身做 reload（enable/disable 已存在 skill 有用，新增不行）。

## Design

### 1. Skill 檔案位置 + frontmatter

路徑：`agents/skills/ask-assistant/SKILL.md`

```yaml
---
name: ask-assistant
description: Use when answering a general question in a Slack thread triggered by `@bot ask` — covers architecture/behavior/design questions, thread summaries, concept clarifications, tradeoff analysis, and code walkthroughs (with or without an attached repo). Enforces strict read-only boundaries and redirects bug triage to `@bot issue` and PR reviews to `@bot review`. Do NOT use for filing issues, posting PR reviews, committing code, or off-topic queries like translation / creative writing / casual chat.
---
```

`description` 的關鍵字（`@bot ask` / `thread` / `architecture` / `tradeoff` / `read-only` / `redirect`）幫 agent 在有 Issue / PR Review 同場的 skill list 時做出正確選擇。結尾的 `Do NOT use for …` 明確排除干擾。

### 2. Ask goal 加 skill handshake

`app/config/defaults.go` 的 `defaultAskGoal` 從：

```go
defaultAskGoal = "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output ===ASK_RESULT=== followed by JSON {\"answer\": \"<markdown>\"}."
```

改為：

```go
defaultAskGoal = "Answer the user's question using the thread, and (if a codebase is attached) the repo. Follow the ask-assistant skill for scope, boundaries, and punt rules. Output ===ASK_RESULT=== followed by JSON {\"answer\": \"<markdown>\"}."
```

與 `defaultIssueGoal` / `defaultPRReviewGoal` 的 `Use the <name> skill` 慣例對齊。

### 3. Body 大綱

寫作原則：**agent-agnostic 自然語言 SOP**。不假設特定 agent 的 tool 呼叫語法（不寫 `Bash(...)` 這類 claude 標記）、不綁 runtime 行為（例如 tool_use 事件）。純政策 + 範例輸出導向，claude / opencode / codex / gemini 都能理解。

Body 分六節，大致 150–220 行：

**#1. Input**
一段話說明 agent 會收到什麼 prompt 內容（thread_context、可能的 repo、extra_description、channel/reporter、language、output_rules）。

**#2. Classification（分類決定走哪條 SOP）**

教 agent 分三類。**優先序：問題意圖 > 附件狀態**——即使有 repo，如果問題是 thread-oriented 的（摘要、概念、決策），也走 Pure-thread；反過來沒 repo 但問題是 code-oriented 也能靠 thread 裡的 code 片段盡力答。

- *Pure-thread*：問題是對話摘要、概念釐清、建議、tradeoff 分析 → 走 §3a；有 repo 也別主動探。
- *Codebase*：問題明確是 code / 架構 / 行為 / 「X 在哪」，且有 repo 可探 → 走 §3b。
- *Punt-worthy*：命中 §5 主題邊界規則 → 走 §5。

**#3a. Pure-thread SOP**

- 從 thread_context 提取關鍵事實，不擅自臆造。
- 若 thread 資訊不足：直接承認「thread 沒有足夠線索判斷 X」，不硬答。
- 結構化回覆：*簡答* → *依據* → *延伸*（只在有真貨時才寫延伸）。
- **Mantis 擷取**：thread 出現 Mantis URL 格式（`view.php?id=<N>` 或 `/issues/<N>`）時，先呼叫 `mantis` skill `get-issue <N>` 抓 ticket 細節再回答；若 `status` 報 `auth_failed` 就放棄擷取，靠 URL + thread 原文答。跟 `triage-issue` §1a 一致。

**#3b. Codebase SOP**

- **允許的輕量查詢**：讀檔 / 列目錄 / 文字搜尋 / git 歷史查詢 / 符號查找這類 *只讀 + 短時* 的操作。
- **禁止**：任何會產生狀態變化或長耗時的指令（見 §4）。需要深度分析就走 §5 punt。
- 引用一律 `path/to/file.ext:LINE` 格式；> 3 處用清單呈現。
- 不確定就明講不確定——不補編故事。

**#4. 動作邊界（A 類，hard no）**

- *Read-only repo*：不寫任何檔案（包括 temp）、不改 git 狀態（no commit / push / branch / reset）、不 rebase / cherry-pick。
- *不開票、不 review*：不建 issue、不建 PR、不提交 PR review、不在 issue/PR 留 comment。`gh` 指令限制在 read 子集（`view` / `list` / `search` / `api GET` 類）。
- *不跑耗時指令*：test suite / build / long-running processes 禁跑；預期 > 10 秒或需要大量網路 I/O 的指令不跑。要深度分析就 punt 到 `@bot issue`。
- *不碰 secrets*：不讀 `.env*` 類檔案、不把 env var value 印進輸出、不 `printenv`。
- *不外連*：不 `curl` 任意外部 URL / 不發 webhook；skill（如 mantis）提供的受控通道除外。

> **Enforcement note**：這些是 prompt-level 規則。Agent 環境仍帶 `GH_TOKEN`（公司情境 Ask 多數需讀 repo，砍 token 就廢了），物理上能越權。誤用靠 audit log 事後發現 + skill 文案修正，不靠 runtime 擋。

**#5. 主題邊界（B 類，軟性引導 + 純偏題婉拒）**

*策略：先盡力回答可見層面，結尾加一句建議。*

| 觸發條件 | 建議 closing 一句 |
|----------|-------------------|
| 有 stack trace / 明講「壞了」/ 需要追 root cause | 「想追完整 root cause + TDD fix plan 請改用 `@bot issue`」|
| 貼了 PR URL 要求檢查 | 「想要 line-level review 請改用 `@bot review <url>`」|
| 想要我改 code / 寫 patch | 「實際改動請開 issue 讓 worker 正式處理」|

*純偏題*（例外情境，直接婉拒，不硬答）：

- 閒聊（天氣、食物、星座、八卦）
- 翻譯、代寫（email、履歷、情書、行銷文案）
- 通用 LLM 挑戰題（haiku、謎題、roleplay）

→ 統一回：「這超出我的職責範圍（工程/專案相關問題為主）。需要一般對話協助請使用一般 LLM。」

**#6. Output 對齊**

引用 workflow 已經設定的 output_rules：Slack mrkdwn / 無 heading marker / `ASK_RESULT` 包 JSON / ≤30000 chars。Skill 不重複這些規則，只強調「不要破壞」。

### 4. `skills.yaml` 註冊

追加一段：

```yaml
skills:
  # ... existing ...
  ask-assistant:
    type: local
    path: agents/skills/ask-assistant
```

### 5. `ask.go` 清理 dead code

BuildJob 內的：

```go
// Skills intentionally nil — Ask flow defensive until empty-dir skill
// spike (Phase 4) observed-safe for a release cycle.
Skills: nil,
```

直接移除。理由：`app/app.go:286` 的 `job.Skills = loadSkills(ctx, skillLoader, appLogger)` 會覆蓋所有 workflow 的 `Skills` 欄位，這個 `nil` 根本沒生效。保留只會讓下一個讀 code 的人誤以為 Ask 沒載 skill。

## 驗收（Behavioral Canaries）

部署後手動跑 4 個 canary query，觀察輸出是否符合 skill 規則。不依賴 log instrumentation——agent 有沒有實際 consult skill 由行為判定。

1. `@bot ask 翻譯 hello world 成中文` → agent 婉拒，回覆含「超出職責範圍」。
2. `@bot ask` + thread 有 Mantis URL「這張單的狀況如何？」→ agent 呼叫 mantis skill 抓 ticket，回覆含 ticket 內容或明確說明抓不到（auth_failed）。
3. `@bot ask` + 附 repo「BuildJob 在哪個檔？」→ 回覆含 `path/to/file.go:NN` 格式引用。
4. `@bot ask` + thread 有 stack trace「這壞了」→ agent 給初步推論，結尾句引導 `@bot issue`。

附加檢查（手看 GitHub / git log）：部署後一週內 Ask agent 沒在任何 repo 創過 issue / PR / commit。

## 部署

1. 新增 skill 檔 `agents/skills/ask-assistant/SKILL.md`（按 §1 + §3 body 大綱撰寫）。
2. `app/config/defaults.go` 更新 `defaultAskGoal`（§2）。
3. `app/workflow/ask.go` 的 BuildJob 移除 `Skills: nil` 指派與註解（§5）。
4. 更新 `skills.yaml` 加 `ask-assistant` 一段（§4）。
5. **重啟 app**（新增 local skill 必須重啟才能生效；`bakedIn` 只在 `NewLoader` 啟動時掃目錄一次）。
6. 手動跑「驗收」4 個 canary query 驗證行為。

## Rollback

Skill 是純資源檔 + 一行 goal 文案改動，rollback 路徑：

- 從 `skills.yaml` 拿掉 `ask-assistant` 那段 → watcher reload 後 agent 就看不到這個 skill。
- `defaultAskGoal` 還原為原始字串（一行 git revert 即可）。
- 如果需要完整移除，刪 `agents/skills/ask-assistant/` 資料夾並重啟 app。

不涉及 schema / runtime / wire change，沒有 migration 成本。
