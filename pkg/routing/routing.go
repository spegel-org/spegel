package routing

import (
	"context"
	"net/netip"
)

// Router implements the discovery of content.
type Router interface {
	// Ready returns true when the router is ready.
	Ready(ctx context.Context) (bool, error)
	// Resolve asynchronously discovers addresses that can serve the content defined by the give key.
	Resolve(ctx context.Context, key string, count int) (<-chan netip.AddrPort, error)
	// Advertise broadcasts that the current router can serve the content.
	Advertise(ctx context.Context, keys []string) error
}
