package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// prURLRe captures owner/repo/number from a canonical GitHub PR URL.
// Only github.com is accepted (spec §Non-goals: enterprise deferred to v2).
var prURLRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/pull/(\d+)(?:[/?#].*)?$`)

// prURLScanRe is the same pattern without anchors, for substring search.
var prURLScanRe = regexp.MustCompile(`https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/pull/\d+`)

// PRURLParts captures the components of a parsed GitHub PR URL.
type PRURLParts struct {
	URL    string
	Owner  string
	Repo   string
	Number int
}

// ParsePRURL validates syntactic shape + extracts parts. Does NOT touch
// GitHub — use GitHubPR.GetPullRequest for existence / accessibility.
func ParsePRURL(url string) (PRURLParts, error) {
	m := prURLRe.FindStringSubmatch(url)
	if m == nil {
		return PRURLParts{}, fmt.Errorf("not a valid github.com PR URL: %q", url)
	}
	num, err := strconv.Atoi(m[3])
	if err != nil {
		return PRURLParts{}, fmt.Errorf("non-numeric PR number: %s", m[3])
	}
	return PRURLParts{URL: url, Owner: m[1], Repo: m[2], Number: num}, nil
}

// ScanThreadForPRURL returns the first PR URL found anywhere in the given
// messages, or ("", false) if none. Strips Slack <...> wrapping.
func ScanThreadForPRURL(msgs []string) (string, bool) {
	for _, m := range msgs {
		unwrapped := unwrapSlackURLs(m)
		if loc := prURLScanRe.FindStringIndex(unwrapped); loc != nil {
			return unwrapped[loc[0]:loc[1]], true
		}
	}
	return "", false
}

// unwrapSlackURLs strips Slack's <...> URL wrapping from inline mentions.
//
//	<https://...>          → https://...
//	<https://...|display>  → https://...
func unwrapSlackURLs(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '<' {
			if end := strings.IndexByte(s[i+1:], '>'); end >= 0 {
				inner := s[i+1 : i+1+end]
				if pipe := strings.IndexByte(inner, '|'); pipe >= 0 {
					inner = inner[:pipe]
				}
				b.WriteString(inner)
				i = i + 1 + end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
