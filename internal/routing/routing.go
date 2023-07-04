package routing

import (
	"context"
	"time"
)

const KeyTTL = 10 * time.Minute

type Router interface {
	Close() error
	Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan string, error)
	Advertise(ctx context.Context, keys []string) error
	HasMirrors() (bool, error)
}
