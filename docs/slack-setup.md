# Slack App 設定

[English](slack-setup.en.md)

## Scopes 與事件

Bot Token Scopes：
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- 私人頻道：`groups:history`, `groups:read`
- 附件下載：`files:read`

Event Subscriptions：
- `app_mention`
- auto-bind：`member_joined_channel`, `member_left_channel`

Interactivity：
- 啟用（用於 repo/branch 選擇按鈕 + 取消按鈕 + 補充說明 modal）

Slash Command：
- `/triage`

Socket Mode 啟用，App-Level Token scope `connections:write`。

## 用 Manifest 快速建立 App

https://api.slack.com/apps → **Create New App** → **From a manifest**，貼以下 YAML：

```yaml
display_information:
  name: AgentDock
  description: Turn Slack threads into GitHub issues
  background_color: "#1f2937"

features:
  bot_user:
    display_name: AgentDock
    always_online: true
  slash_commands:
    - command: /triage
      description: Triage current thread into a GitHub issue
      usage_hint: "(run inside a thread)"
      should_escape: false

oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - channels:read
      - groups:history
      - groups:read
      - chat:write
      - commands
      - files:read
      - users:read

settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - member_joined_channel
      - member_left_channel
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
  token_rotation_enabled: false
```

建立後：
1. **Basic Information** → **App-Level Tokens** → **Generate** → 加 `connections:write` scope → 複製 `xapp-…` 作為 `SLACK_APP_TOKEN`
2. **Install App** → 複製 `xoxb-…` 作為 `SLACK_BOT_TOKEN`
