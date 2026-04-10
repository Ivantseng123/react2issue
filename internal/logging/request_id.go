package logging

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewRequestID generates a short, time-based request ID: YYYYMMDD-HHmmss-xxxx.
func NewRequestID() string {
	b := make([]byte, 2)
	rand.Read(b)
	return fmt.Sprintf("%s-%04x", time.Now().Format("20060102-150405"), b)
}
