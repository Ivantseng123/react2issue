package connectivity

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CheckRedis connects to Redis at addr (with optional auth + TLS) and runs
// a PING to verify reachability. Returns nil on success, an error otherwise.
func CheckRedis(addr, password string, db int, useTLS bool) error {
	if addr == "" {
		return errors.New("address is empty")
	}
	opts := &redis.Options{Addr: addr, Password: password, DB: db}
	if useTLS {
		opts.TLSConfig = &tls.Config{}
	}
	c := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return c.Close()
}
