// Package redis creates the go-redis client.
package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

func New(ctx context.Context, url string) (*goredis.Client, error) {
	opt, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}
	c := goredis.NewClient(opt)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return c, nil
}
