package config

import (
	"os"
	"strings"
)

// EnvOverrideMap returns a koanf-friendly map of env var values used by the
// app module. Unset env vars are absent from the result.
func EnvOverrideMap() map[string]any {
	out := map[string]any{}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		out["slack.bot_token"] = v
	}
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		out["slack.app_token"] = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		out["github.token"] = v
	}
	if v := os.Getenv("MANTIS_API_TOKEN"); v != "" {
		out["mantis.api_token"] = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		out["redis.addr"] = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		out["redis.password"] = v
	}
	if v := os.Getenv("SECRET_KEY"); v != "" {
		out["secret_key"] = v
	}
	return out
}

// scanSecretEnvVars picks up AGENTDOCK_SECRET_* env vars.
func scanSecretEnvVars() map[string]string {
	const prefix = "AGENTDOCK_SECRET_"
	out := make(map[string]string)
	for _, env := range os.Environ() {
		if idx := strings.Index(env, "="); idx > 0 {
			key := env[:idx]
			if strings.HasPrefix(key, prefix) {
				name := key[len(prefix):]
				if name != "" {
					out[name] = env[idx+1:]
				}
			}
		}
	}
	return out
}
