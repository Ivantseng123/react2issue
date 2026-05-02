package githubapp

import (
	"crypto/rsa"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestSource(t *testing.T, srv *httptest.Server, key *rsa.PrivateKey, nowFn func() time.Time) *appInstallationSource {
	t.Helper()
	if nowFn == nil {
		fixed := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return fixed }
	}
	return &appInstallationSource{
		appID:          1234,
		installationID: 5678,
		privateKey:     key,
		httpClient:     srv.Client(),
		baseURL:        srv.URL,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:            nowFn,
	}
}

// mintHandler returns a handler that responds with sequential tokens
// (token-1, token-2, ...) and expiry = now + 60min, counting hits.
func mintHandler(now func() time.Time, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		expires := now().Add(60 * time.Minute).UTC().Format(time.RFC3339)
		body := fmt.Sprintf(`{"token":"token-%d","expires_at":%q}`, n, expires)
		_, _ = w.Write([]byte(body))
	}
}

func TestStaticPATSource_BothMethodsReturnSameToken(t *testing.T) {
	s := &staticPATSource{token: "ghp_static"}
	got1, err := s.Get()
	if err != nil || got1 != "ghp_static" {
		t.Errorf("Get() = (%q, %v)", got1, err)
	}
	got2, err := s.MintFresh()
	if err != nil || got2 != "ghp_static" {
		t.Errorf("MintFresh() = (%q, %v)", got2, err)
	}
}

func TestAppInstallationSource_GetMintsThenCaches(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	tok1, err := src.Get()
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if tok1 != "token-1" {
		t.Errorf("first Get token = %q, want token-1", tok1)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after first Get: hits = %d, want 1", got)
	}

	tok2, err := src.Get()
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if tok2 != "token-1" {
		t.Errorf("second Get token = %q, want cached token-1", tok2)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after second Get: hits = %d, want 1 (cache hit, no extra mint)", got)
	}
}

func TestAppInstallationSource_GetReMintsWhenCacheNearExpiry(t *testing.T) {
	hits := &atomic.Int32{}
	currentNow := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return currentNow }

	srv := httptest.NewServer(mintHandler(nowFn, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), nowFn)

	if _, err := src.Get(); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits after first Get = %d", got)
	}

	// Advance time so cache has < 50min remaining (token expires at +60min,
	// move now forward 11min → 49min remaining → must re-mint).
	currentNow = currentNow.Add(11 * time.Minute)

	tok, err := src.Get()
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	if tok != "token-2" {
		t.Errorf("got %q, want token-2 (re-mint)", tok)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestAppInstallationSource_MintFreshBypassesCacheAndUpdates(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	if _, err := src.Get(); err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	tok, err := src.MintFresh()
	if err != nil {
		t.Fatalf("MintFresh: %v", err)
	}
	if tok != "token-2" {
		t.Errorf("MintFresh token = %q, want token-2", tok)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2 (MintFresh bypasses cache)", got)
	}

	// Subsequent Get should now hit the cache populated by MintFresh.
	tok2, err := src.Get()
	if err != nil {
		t.Fatalf("post-MintFresh Get: %v", err)
	}
	if tok2 != "token-2" {
		t.Errorf("post-MintFresh Get = %q, want cached token-2", tok2)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want still 2", got)
	}
}

func TestAppInstallationSource_ConcurrentGetMintFreshNoRace(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if _, err := src.Get(); err != nil {
					t.Errorf("goroutine %d Get: %v", i, err)
				}
			} else {
				if _, err := src.MintFresh(); err != nil {
					t.Errorf("goroutine %d MintFresh: %v", i, err)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestAppInstallationSource_MintErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), nil)

	if _, err := src.Get(); err == nil {
		t.Error("expected error on 401")
	}
	if _, err := src.MintFresh(); err == nil {
		t.Error("expected error on 401 from MintFresh")
	}
}
