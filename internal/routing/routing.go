package routing

import (
	"context"
	"net/netip"
	"time"
)

const KeyTTL = 10 * time.Minute

type Router interface {
	Close() error
	Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan netip.AddrPort, error)
	Advertise(ctx context.Context, keys []string) error
	HasMirrors() (bool, error)
}
