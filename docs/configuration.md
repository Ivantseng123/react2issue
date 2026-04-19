# 設定

[English](configuration.en.md)

AgentDock v2 的 config 拆成兩個檔案：

- [App 設定（configuration-app.md）](configuration-app.md) — Slack bot、channels、rate limit、Mantis、prompt 指示
- [Worker 設定（configuration-worker.md）](configuration-worker.md) — agents、providers、worker count、repo cache

如果你從 v1 升級，請看 [MIGRATION-v2.md](MIGRATION-v2.md)。

## 快速開始

```bash
agentdock init app -i       # 建立 ~/.config/agentdock/app.yaml，互動式問 Slack/GitHub/Redis
agentdock init worker -i    # 建立 ~/.config/agentdock/worker.yaml，問 GitHub/Redis/secret/providers
```

然後：

```bash
# Redis 模式（建議 production）
agentdock app -c ~/.config/agentdock/app.yaml            # 另一台機器跑 worker
agentdock worker -c ~/.config/agentdock/worker.yaml

# Inmem 模式（單機測試）— app 會自動起本地 worker pool
agentdock app -c ~/.config/agentdock/app.yaml \
              --worker-config ~/.config/agentdock/worker.yaml
```

預設 `queue.transport` 是 `inmem`；要切 Redis 請設 `queue.transport: redis` 並填 `redis.addr`。
