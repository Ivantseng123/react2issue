---
date: 2026-04-21
status: approved
owners: Ivantseng123
---

# Bot 訊息在 Thread Context 正確保留

## Problem

目前 thread context 讀取時，外部 bot（GitHub / Jira / webhook / integration）發的訊息整批被濾掉，agent 看不到。使用者回報：「訊息如果是另外一個 bot 發的，會直接讀不到。」

## Root Cause

兩處獨立缺陷疊加：

1. `app/bot/workflow.go:400` 將 `botUserID` 寫死為 `""`，從未把真正的 bot 身份傳進 `FetchThreadContext`。
2. `app/slack/client.go:481` 的過濾條件：

   ```go
   if m.BotID != "" || m.User == botUserID {
       continue
   }
   ```

   前半段殺光所有 `BotID` 非空的訊息（包含外部 bot）；後半段配空字串等於空砲。

實際行為等同於「所有 bot 訊息一律濾除」，而設計原意是「只濾掉自己」。

## Goals

- 外部 bot 訊息（其他 integration / webhook）進入 thread context，agent 可讀取。
- 自己這個 bot 發的訊息（status update、selector、result）仍然不進 context，避免 feedback loop。
- Bot 訊息的文字內容（常見於 `attachments` / `blocks`，`m.Text` 為空）能正確擷取。
- Bot 顯示名（`GitHub`、`Jira` 等）在 context 裡被識別，取代 user id 位置。

## Non-Goals

- 不提供 allowlist / denylist 來選擇性納入 bot。所有非自己的 bot 一律保留。
- 不處理人類訊息的 Slack unfurl preview（attachments 副本）— 範圍控制在 bot 訊息。
- 不動 worker 或 shared 模組；不新增 config。

## Design

### 1. Bot 身份傳遞

新增 `Identity` struct：

```go
// app/bot/workflow.go 或新檔 app/bot/identity.go
type Identity struct {
    UserID string // e.g. "UBOTxxxxx" — 對應 auth.test 的 user_id
    BotID  string // e.g. "BBOTxxxxx" — 對應 auth.test 的 bot_id
}
```

調整時序：`app/app.go` 裡把 `api.AuthTest()` 呼叫搬到 `NewWorkflow()` 之前，成功時把 `{UserID, BotID}` 組成 `Identity` 傳入建構子；失敗時傳 zero value（`Identity{}`）—— filter 行為等於「不濾任何東西」，比現狀全濾好。

```go
// app/app.go（重排後）
botIdentity := bot.Identity{}
if authResp, err := api.AuthTest(); err == nil {
    botIdentity = bot.Identity{UserID: authResp.UserID, BotID: authResp.BotID}
    appLogger.Info("Bot 身份已解析", "phase", "處理中",
        "user_id", botIdentity.UserID, "bot_id", botIdentity.BotID)
} else {
    appLogger.Warn("Bot 身份解析失敗，thread filter 將保留所有訊息", "phase", "失敗", "error", err)
}

wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, mantisClient,
    coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, botIdentity)
```

`Workflow` struct 增加 `identity Identity` 欄位；`workflow.go:400` 改為：

```go
rawMsgs, err := w.slack.FetchThreadContext(
    pt.ChannelID, pt.ThreadTS, pt.TriggerTS,
    w.identity.UserID, w.identity.BotID,
    w.cfg.MaxThreadMessages,
)
```

### 2. Filter 條件修正

`filterThreadMessages` signature 與邏輯：

```go
func filterThreadMessages(
    messages []slack.Message,
    triggerTS, botUserID, botID string,
) []ThreadRawMessage {
    var result []ThreadRawMessage
    for _, m := range messages {
        if m.Timestamp >= triggerTS {
            continue
        }
        // 只濾掉自己
        if botUserID != "" && m.User == botUserID {
            continue
        }
        if botID != "" && m.BotID == botID {
            continue
        }
        // 其他 bot、webhook、integration 一律保留
        user := m.User
        if m.BotID != "" {
            if name := resolveBotDisplayName(m); name != "" {
                user = name
            }
        }
        result = append(result, ThreadRawMessage{
            User:      user,
            Text:      extractMessageText(m),
            Timestamp: m.Timestamp,
            Files:     m.Files,
        })
    }
    return result
}
```

**為什麼 `m.User == botUserID` 和 `m.BotID == botID` 都要檢查？**
自己的 bot 訊息在多數情況下兩個欄位都會命中，但某些 Slack 路徑（`chat.postMessage` 帶 custom `username`、thread_broadcast、新 block API）會導致 `m.User` 為空或不一致。雙條件 OR 是保險。

### 3. Text 擷取

新增 `extractMessageText` helper：

```go
// extractMessageText returns m.Text if non-empty, otherwise reconstructs
// text from bot attachments/blocks. Returns "" only if the message has
// no renderable text content.
func extractMessageText(m slack.Message) string {
    if strings.TrimSpace(m.Text) != "" {
        return m.Text
    }
    if s := extractFromAttachments(m.Attachments); s != "" {
        return s
    }
    if s := extractFromBlocks(m.Blocks.BlockSet); s != "" {
        return s
    }
    return ""
}
```

**Attachments**（舊 API，webhook / 舊版 integration 多用此）：

依序串接 `Pretext`、`Title`、`Text`、`Fallback`；若有 `Fields`，渲染成 `*Title*: Value`；多個 attachment 以空行分隔。非空片段用 `\n` 串接。

**Blocks**（新 API，新版 GitHub / Jira）：

遍歷 `SectionBlock` / `HeaderBlock` / `ContextBlock` / `RichTextBlock`，抓 `TextBlockObject.Text`（`plain_text` 與 `mrkdwn` 皆處理）。其他 block type（`ImageBlock`、`ActionBlock`、`DividerBlock`）略過。

**順序語意為二擇一**：有 `Text` 用 `Text`；沒 `Text` 但有 Attachments 用 Attachments；都沒才看 Blocks。Integration 通常只用其中一種，同時抓會重複。

若三者皆空（極少見，純 reaction 或互動訊息），回 `""`，但訊息 **仍保留**在 `rawMsgs` — 下游決定要不要顯示，debug 時也看得到。

### 4. Bot 顯示名

`resolveBotDisplayName` 優先序：

```go
func resolveBotDisplayName(m slack.Message) string {
    if m.BotProfile != nil && m.BotProfile.Name != "" {
        return m.BotProfile.Name  // 最準，如 "GitHub" / "Jira"
    }
    if m.Username != "" {
        return m.Username          // 次選，integration 自填
    }
    if m.BotID != "" {
        return m.BotID             // 至少是個 id
    }
    return ""
}
```

`ThreadRawMessage.User` 對 bot 訊息塞顯示名而非 Slack user id。下游 `workflow.go:417` 呼叫 `w.slack.ResolveUser(m.User)`，該路徑內部 `GetUserInfo` 對非 `Uxxx` 格式字串會失敗並 fallback 回原字串（`client.go:181-191`），剛好輸出 `"GitHub"`。下游零改動。

需在 `filterThreadMessages` 加註解說明：「User 欄位對 bot 訊息是顯示名，依賴 `ResolveUser` 的 error-fallback 行為；如未來改動 `ResolveUser`，須同步檢視此處。」

## Testing

### 擴充 `TestFilterThreadMessages`

新增案例：

- 外部 bot 訊息（`BotID != ""`, `User == ""`）→ 保留
- 自己 bot（`User == botUserID`）→ 濾掉
- 自己 bot（`BotID == botBotID` 但 `User` 對不上）→ 濾掉
- Bot 訊息 `Text` 空但 attachments 有內容 → `ThreadRawMessage.Text` 非空
- Bot 訊息 `Text` 空但 blocks 有內容 → `ThreadRawMessage.Text` 非空
- Bot 訊息三者皆空 → 保留，`Text == ""`
- `BotProfile.Name` / `Username` / `BotID` 三選一優先序各一案例
- `botUserID == ""` 且 `botID == ""`（AuthTest 失敗 fallback）→ 全部保留

### 新增獨立測試

- `TestExtractMessageText`：
  - `Text` 非空（即使 attachments 亦有內容）直接回 `Text`
  - 僅 attachments
  - 僅 blocks（SectionBlock、HeaderBlock、ContextBlock、RichTextBlock 各一）
  - Attachment 有 `Fields` → 渲染 `*Title*: Value`
  - 三者皆空 → 回 `""`
- `TestResolveBotDisplayName`：三段優先序各一案例。

### 介面簽章調整

以下因 `botID` 新增而更新簽章：

- `app/slack/client.go:439` `FetchThreadContext`
- `app/bot/workflow.go:38` `slackAPI` interface
- `app/bot/workflow_test.go:115` `stubSlack.FetchThreadContext`

## Files Changed

| 檔案 | 動作 |
|------|------|
| `app/slack/client.go` | `FetchThreadContext` 多接 `botID` 參數；`filterThreadMessages` 改 filter 條件 + 呼叫 helper；新增 `extractMessageText` / `extractFromAttachments` / `extractFromBlocks` / `resolveBotDisplayName` |
| `app/slack/client_test.go` | 擴充 `TestFilterThreadMessages`；新增 `TestExtractMessageText`、`TestResolveBotDisplayName` |
| `app/bot/workflow.go` | `slackAPI.FetchThreadContext` 加 `botID`；`Workflow` 加 `identity Identity`；`NewWorkflow` 多接 `Identity`；第 400 行改用 `w.identity` |
| `app/bot/identity.go` （新檔）| 定義 `Identity` struct |
| `app/bot/workflow_test.go` | `stubSlack.FetchThreadContext` 對齊；`NewWorkflow` 呼叫補 zero-value `Identity` |
| `app/app.go` | `AuthTest()` 時序提前到 `NewWorkflow()` 前；同時抓 `UserID` 與 `BotID`；傳入 `NewWorkflow` |

無 config 變更；import direction 零影響（改動皆在 `app/` 內）。

## Out of Scope

- Allowlist / denylist 選擇性保留 bot（目前一律保留非自己的 bot）。
- 人類訊息的 Slack unfurl 擷取（本次僅 bot 訊息）。
- Mantis 連結擷取（另一份 spec）。
