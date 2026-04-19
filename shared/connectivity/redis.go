package connectivity

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// CheckRedis connects to a Redis server at addr and issues PING.
// Uses a 5 second timeout. Empty addr returns an error.
func CheckRedis(addr string) error {
	if addr == "" {
		return errors.New("address is empty")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return client.Ping(ctx).Err()
}
