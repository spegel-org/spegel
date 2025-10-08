package routing

import "testing"

func TestClosableBalancer(t *testing.T) {
	t.Parallel()

	// Test that we can call close multiple times.
	cb := NewClosableBalancer(NewRoundRobin())
	for range 3 {
		cb.Close()
	}
}
