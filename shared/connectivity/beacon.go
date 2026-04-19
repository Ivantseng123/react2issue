package connectivity

import (
	"context"
	"time"

	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/redis/go-redis/v9"
)

// VerifySecretBeacon reads the beacon from redis and checks it matches
// the given key. Returns nil if match (or if no beacon has been written
// yet — see shared/crypto.VerifyBeacon), error otherwise. Applies a 5
// second timeout to the whole operation.
func VerifySecretBeacon(rdb *redis.Client, key []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return crypto.VerifyBeacon(ctx, rdb, key)
}
