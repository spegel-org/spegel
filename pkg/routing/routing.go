package routing

import (
	"context"
)

// Router implements the discovery of content.
type Router interface {
	// Ready returns true when the router is ready.
	Ready(ctx context.Context) (bool, error)
	// Lookup discovers peers with the given key and returns a balancer with the peers.
	Lookup(ctx context.Context, key string, count int) (Balancer, error)
	// Advertise broadcasts that the current router can serve the content.
	Advertise(ctx context.Context, keys []string) error
}
