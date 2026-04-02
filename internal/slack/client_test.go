package slack

import (
	"testing"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		minWords int
	}{
		{
			name:     "short message",
			message:  "login page crashes",
			minWords: 2,
		},
		{
			name:     "long message with stop words",
			message:  "The user is unable to login after the latest deploy and the page shows a white screen",
			minWords: 3,
		},
		{
			name:     "empty message",
			message:  "",
			minWords: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := ExtractKeywords(tt.message)
			if len(kw) < tt.minWords {
				t.Errorf("expected at least %d keywords, got %d: %v", tt.minWords, len(kw), kw)
			}
			stopWords := map[string]bool{"the": true, "is": true, "a": true, "and": true, "to": true, "after": true}
			for _, w := range kw {
				if stopWords[w] {
					t.Errorf("keyword %q is a stop word", w)
				}
			}
		})
	}
}
