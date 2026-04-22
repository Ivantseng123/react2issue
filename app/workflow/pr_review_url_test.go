package workflow

import "testing"

func TestParsePRURL_Valid(t *testing.T) {
	cases := []struct {
		in         string
		wantOwner  string
		wantRepo   string
		wantNumber int
	}{
		{"https://github.com/foo/bar/pull/7", "foo", "bar", 7},
		{"https://github.com/Ivantseng123/agentdock/pull/117", "Ivantseng123", "agentdock", 117},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePRURL(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Owner != tc.wantOwner || got.Repo != tc.wantRepo || got.Number != tc.wantNumber {
				t.Errorf("got {%s %s %d}", got.Owner, got.Repo, got.Number)
			}
		})
	}
}

func TestParsePRURL_Invalid(t *testing.T) {
	cases := []string{
		"",
		"github.com/foo/bar/pull/7",           // missing protocol
		"https://example.com/foo/bar/pull/7",  // not github.com
		"foo/bar#7",                           // shortened form
		"https://github.com/foo/bar/issues/7", // issues not pull
		"https://github.com/foo/bar/pull/abc", // non-numeric
		"https://github.com/foo/bar/pull/",    // no number
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ParsePRURL(tc); err == nil {
				t.Errorf("expected error for %q", tc)
			}
		})
	}
}

func TestScanThreadForPRURL(t *testing.T) {
	msgs := []string{
		"no URL here",
		"please review https://github.com/foo/bar/pull/10 thanks",
		"follow-up discussion",
	}
	got, ok := ScanThreadForPRURL(msgs)
	if !ok {
		t.Fatal("expected match")
	}
	if got != "https://github.com/foo/bar/pull/10" {
		t.Errorf("got %q", got)
	}
}
