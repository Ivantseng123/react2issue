package githubapp

import (
	"crypto/rsa"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// TokenSource provides GitHub auth tokens for the app process. Two methods
// because the two callers have different freshness needs: app-internal
// clients can tolerate cached tokens (Get), while job dispatch wants a
// just-minted one so workers receive close to the full 60min TTL
// (MintFresh).
type TokenSource interface {
	// Get returns a token, possibly cached. Caller accepts that the
	// returned token may have as little as 50 minutes remaining TTL.
	Get() (string, error)

	// MintFresh always mints a new installation token, bypassing any
	// cache, and updates the cache with the new value.
	MintFresh() (string, error)
}

// staticPATSource adapts a personal access token to the TokenSource
// interface so the dispatch path can call MintFresh in both modes
// without branching.
type staticPATSource struct{ token string }

func (s *staticPATSource) Get() (string, error)       { return s.token, nil }
func (s *staticPATSource) MintFresh() (string, error) { return s.token, nil }

// appInstallationSource mints GitHub installation tokens via the
// /app/installations/{id}/access_tokens endpoint and caches the result
// until expiry approaches (50min remaining = 60min TTL minus 10min
// safety buffer).
type appInstallationSource struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	httpClient     *http.Client
	baseURL        string
	logger         *slog.Logger
	now            func() time.Time

	mu        sync.Mutex
	cached    string
	expiresAt time.Time
}

// minCachedTTL is the floor TTL that Get is allowed to hand back. With a
// 60min installation-token TTL this leaves a 10min safety buffer for the
// caller's request to complete before the token expires.
const minCachedTTL = 50 * time.Minute

func (s *appInstallationSource) Get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" && s.expiresAt.Sub(s.now()) >= minCachedTTL {
		return s.cached, nil
	}
	return s.mintLocked()
}

func (s *appInstallationSource) MintFresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mintLocked()
}

// mintLocked must be called with s.mu held. Updates cache on success.
func (s *appInstallationSource) mintLocked() (string, error) {
	jwtStr, err := signJWT(s.privateKey, s.appID, s.now())
	if err != nil {
		return "", err
	}
	token, expiresAt, err := postInstallationToken(s.httpClient, s.baseURL, jwtStr, s.installationID)
	if err != nil {
		return "", err
	}
	s.cached = token
	s.expiresAt = expiresAt
	s.logger.Info("installation token minted",
		"phase", "完成",
		"installation_id", s.installationID,
		"expires_at", expiresAt.Format(time.RFC3339),
	)
	return token, nil
}
