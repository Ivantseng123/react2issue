# Slack App Setup

[繁體中文](slack-setup.md)

## Scopes & Events

Bot Token Scopes:
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- Private channels: `groups:history`, `groups:read`
- Attachment downloads: `files:read`

Event Subscriptions:
- `app_mention`
- auto-bind: `member_joined_channel`, `member_left_channel`

Interactivity:
- Enabled (for repo/branch selection buttons + cancel button + description modal)

Slash Command:
- `/triage`

Socket Mode enabled, App-Level Token scope `connections:write`.

## Create App from Manifest

https://api.slack.com/apps → **Create New App** → **From a manifest**, paste this YAML:

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

After creation:
1. **Basic Information** → **App-Level Tokens** → **Generate** → add `connections:write` scope → copy `xapp-…` as `SLACK_APP_TOKEN`
2. **Install App** → copy `xoxb-…` as `SLACK_BOT_TOKEN`
