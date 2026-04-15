package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagToKey is the single source of truth mapping cobra flag names (dash-case)
// to koanf dotted keys (which must match the yaml tags on config.Config so
// unmarshal round-trips cleanly). Flags not in this map are ignored by
// buildFlagOverrideMap (e.g. --help, --version, --config, --force, --interactive).
var flagToKey = map[string]string{
	// Persistent flags (apply to all subcommands that use the koanf loader).
	"log-level":                "log_level",
	"redis-addr":               "redis.addr",
	"redis-password":           "redis.password",
	"redis-db":                 "redis.db",
	"redis-tls":                "redis.tls",
	"github-token":             "github.token",
	"mantis-base-url":          "mantis.base_url",
	"mantis-api-token":         "mantis.api_token",
	"mantis-username":          "mantis.username",
	"mantis-password":          "mantis.password",
	"queue-capacity":           "queue.capacity",
	"queue-transport":          "queue.transport",
	"queue-job-timeout":        "queue.job_timeout",
	"queue-agent-idle-timeout": "queue.agent_idle_timeout",
	"queue-prepare-timeout":    "queue.prepare_timeout",
	"queue-status-interval":    "queue.status_interval",
	"logging-dir":              "logging.dir",
	"logging-level":            "logging.level",
	"logging-retention-days":   "logging.retention_days",
	"logging-agent-output-dir": "logging.agent_output_dir",
	"repo-cache-dir":           "repo_cache.dir",
	"repo-cache-max-age":       "repo_cache.max_age",
	"attachments-store":        "attachments.store",
	"attachments-temp-dir":     "attachments.temp_dir",
	"attachments-ttl":          "attachments.ttl",
	"workers":                  "workers.count",
	"active-agent":             "active_agent",
	"providers":                "providers",
	"skills-config":            "skills_config",

	// App-specific flags.
	"slack-bot-token":        "slack.bot_token",
	"slack-app-token":        "slack.app_token",
	"server-port":            "server.port",
	"auto-bind":              "auto_bind",
	"max-concurrent":         "max_concurrent",
	"max-thread-messages":    "max_thread_messages",
	"semaphore-timeout":      "semaphore_timeout",
	"rate-limit-per-user":    "rate_limit.per_user",
	"rate-limit-per-channel": "rate_limit.per_channel",
	"rate-limit-window":      "rate_limit.window",
}

// addPersistentFlags registers flags that apply to all subcommands that load
// config via the koanf provider chain. Kept on rootCmd.PersistentFlags so both
// app and worker (and any future subcommand) inherit them.
func addPersistentFlags(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()

	pf.String("log-level", "", "log level (debug|info|warn|error)")

	// Redis.
	pf.String("redis-addr", "", "Redis address (host:port)")
	pf.String("redis-password", "", "Redis password")
	pf.Int("redis-db", 0, "Redis DB index")
	pf.Bool("redis-tls", false, "use TLS for Redis connection")

	// GitHub.
	pf.String("github-token", "", "GitHub API token")

	// Mantis.
	pf.String("mantis-base-url", "", "Mantis base URL")
	pf.String("mantis-api-token", "", "Mantis API token")
	pf.String("mantis-username", "", "Mantis username (basic auth fallback)")
	pf.String("mantis-password", "", "Mantis password (basic auth fallback)")

	// Queue.
	pf.Int("queue-capacity", 0, "in-memory queue capacity")
	pf.String("queue-transport", "", "queue transport (inmem|redis)")
	pf.Duration("queue-job-timeout", 0, "max wall-clock time per job")
	pf.Duration("queue-agent-idle-timeout", 0, "max idle time for agent without output")
	pf.Duration("queue-prepare-timeout", 0, "max time for prepare phase (clone/checkout)")
	pf.Duration("queue-status-interval", 0, "interval between job status heartbeats")

	// Logging.
	pf.String("logging-dir", "", "directory for rotated log files")
	pf.String("logging-level", "", "file log level")
	pf.Int("logging-retention-days", 0, "days to retain rotated log files")
	pf.String("logging-agent-output-dir", "", "directory for raw agent stdout/stderr")

	// Repo cache.
	pf.String("repo-cache-dir", "", "directory for cached git clones")
	pf.Duration("repo-cache-max-age", 0, "max age before repo cache is refreshed")

	// Attachments.
	pf.String("attachments-store", "", "attachment storage backend")
	pf.String("attachments-temp-dir", "", "temp directory for downloaded attachments")
	pf.Duration("attachments-ttl", 0, "attachment retention TTL")

	// Workers.
	pf.Int("workers", 0, "number of worker goroutines (alias for workers.count)")

	// Agents.
	pf.String("active-agent", "", "active agent name (single-agent mode)")
	pf.StringSlice("providers", nil, "ordered provider chain (comma-separated)")
	pf.String("skills-config", "", "path to skills.yaml")
}

// addAppFlags registers flags that only apply to `agentdock app` (the main
// Slack bot). Placed as local flags so they don't pollute `agentdock worker`
// help output.
func addAppFlags(cmd *cobra.Command) {
	f := cmd.Flags()

	f.String("slack-bot-token", "", "Slack bot token (xoxb-)")
	f.String("slack-app-token", "", "Slack app-level token (xapp-)")
	f.Int("server-port", 0, "HTTP server port for /healthz and /jobs")
	f.Bool("auto-bind", false, "auto-register channels on join")
	f.Int("max-concurrent", 0, "deprecated; prefer --workers")
	f.Int("max-thread-messages", 0, "max thread messages to read for context")
	f.Duration("semaphore-timeout", 0, "worker semaphore acquire timeout")
	f.Int("rate-limit-per-user", 0, "max triggers per user per window")
	f.Int("rate-limit-per-channel", 0, "max triggers per channel per window")
	f.Duration("rate-limit-window", 0, "rate limit sliding window")
}

// buildFlagOverrideMap walks the set of flags that were explicitly changed on
// cmd (including inherited persistent flags) and returns a koanf-style map
// keyed by dotted path. Flags not in flagToKey are skipped silently.
func buildFlagOverrideMap(cmd *cobra.Command) map[string]any {
	out := map[string]any{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		key, ok := flagToKey[f.Name]
		if !ok {
			return
		}
		switch f.Value.Type() {
		case "string":
			if v, err := cmd.Flags().GetString(f.Name); err == nil {
				out[key] = v
			}
		case "int":
			if v, err := cmd.Flags().GetInt(f.Name); err == nil {
				out[key] = v
			}
		case "bool":
			if v, err := cmd.Flags().GetBool(f.Name); err == nil {
				out[key] = v
			}
		case "duration":
			if v, err := cmd.Flags().GetDuration(f.Name); err == nil {
				out[key] = v
			}
		case "stringSlice":
			if v, err := cmd.Flags().GetStringSlice(f.Name); err == nil {
				out[key] = v
			}
		}
	})
	return out
}
