// Package connectivity provides pure connectivity checks to external
// services (GitHub, Slack, Redis). Used during preflight to validate
// tokens and addresses before the main process starts. No dependency on
// agentdock config types.
package connectivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// CheckGitHubToken verifies the token can authenticate and has repository
// access. It returns the authenticated user's login name on success.
//
// Two-step probe: /user (identity) + /user/repos?per_page=1 (scope). This
// catches both "token is expired" and "token lacks repo access" up front.
func CheckGitHubToken(token string) (string, error) {
	if token == "" {
		return "", errors.New("token is empty")
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	doReq := func(url string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		return httpClient.Do(req)
	}

	// Step 1: verify identity.
	resp, err := doReq("https://api.github.com/user")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub /user returned HTTP %d", resp.StatusCode)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decode /user response: %w", err)
	}
	login := user.Login

	// Step 2: verify repository access.
	resp2, err := doReq("https://api.github.com/user/repos?per_page=1")
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	if resp2.StatusCode == http.StatusForbidden || resp2.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("token lacks repository access (user: %s)", login)
	}
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub /user/repos returned HTTP %d", resp2.StatusCode)
	}

	return login, nil
}
