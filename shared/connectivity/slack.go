package connectivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CheckSlackToken verifies the bot token via Slack auth.test API.
// Returns the bot's user_id on success.
func CheckSlackToken(token string) (string, error) {
	if token == "" {
		return "", errors.New("token is empty")
	}
	if !strings.HasPrefix(token, "xoxb-") {
		return "", errors.New("Slack bot token must start with xoxb-")
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if !body.OK {
		return "", fmt.Errorf("auth.test failed: %s", body.Error)
	}
	return body.UserID, nil
}
