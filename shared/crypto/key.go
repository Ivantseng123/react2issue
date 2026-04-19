package crypto

import (
	"encoding/hex"
	"fmt"
)

// DecodeSecretKey validates and decodes a hex-encoded 32-byte AES key (64
// hex characters). Used by app and worker to derive the shared AES-256-GCM
// key from the operator-supplied secret_key string.
func DecodeSecretKey(hexKey string) ([]byte, error) {
	decoded, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("secret_key: invalid hex: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("secret_key: must be 32 bytes (64 hex chars), got %d bytes", len(decoded))
	}
	return decoded, nil
}
