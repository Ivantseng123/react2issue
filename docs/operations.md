# 監控與管理

[English](operations.en.md)

## 查看 Job 狀態

```bash
curl localhost:8180/jobs | jq .
```

回傳：

```json
{
  "queue_depth": 1,
  "total": 2,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
      "age": "45s",
      "agent": {
        "pid": 12345,
        "command": "claude",
        "alive": true,
        "last_event": "tool_use:Read",
        "last_event_age": "3s",
        "tool_calls": 12,
        "files_read": 8,
        "output_bytes": 15360,
        "cost_usd": 0.042
      }
    },
    {
      "id": "req-def456",
      "status": "pending",
      "repo": "org/frontend",
      "age": "10s",
      "position": 1
    }
  ]
}
```

## 手動終止 Job

```bash
curl -X DELETE localhost:8180/jobs/req-abc123
```

## Slack 取消

Submit 後的排隊訊息帶有「取消」按鈕，點擊即可終止。

## 自動保護

| 機制 | 預設值 | 說明 |
|------|--------|------|
| Job timeout | 20m | 整個 job 的最大生命週期 |
| Agent idle timeout | 5m | stream-json agent 無 event 超過此時間自動終止 |
| Prepare timeout | 3m | clone/setup 超時自動終止 |

超時後 bot 會通知 Slack 使用者並清除 dedup，讓使用者可以重新觸發。

## HTTP Endpoints

| Endpoint | Method | 說明 |
|----------|--------|------|
| `/healthz` | GET | Health check |
| `/jobs` | GET | 列出所有 job 狀態（含 agent 追蹤） |
| `/jobs/{id}` | DELETE | 終止指定 job |

## Agent 行為

Agent 收到 prompt 後：
1. 載入 triage-issue skill
2. 探索 codebase（用自己的內建工具）
3. 評估 confidence（low → 拒絕）
4. 輸出結構化 JSON 結果（不直接建 issue）：

```json
{
  "status": "CREATED",
  "title": "Login page broken after 3 failed attempts",
  "body": "## Problem\n...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
```

App 收到結果後：
- `confidence=low` → 不建 issue，通知使用者
- `files=0` 或 `questions>=5` → 建 issue 但不含 triage section
- 正常 → 建完整 issue + 回 Slack thread
