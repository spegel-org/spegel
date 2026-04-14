package channel

import "sync"

// Gate implements a syncrhonization mechanism that enables switching state and waiting for a specific one.
type Gate struct {
	waiters []chan<- any
	state   bool
	mu      sync.RWMutex
}

// NewGate returns a new gate that is closed.
func NewGate() *Gate {
	return &Gate{
		state: false,
	}
}

// State returns true when the gate is open and false when closed.
func (g *Gate) State() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.state
}

// Open opens the gate and informs any waiters.
func (g *Gate) Open() {
	g.set(true)
}

// Close closes the gate and informs any waiters.
func (g *Gate) Close() {
	g.set(false)
}

func (g *Gate) set(state bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.state == state {
		return
	}
	g.state = state

	for _, ch := range g.waiters {
		ch <- nil
	}
	g.waiters = nil
}

// WaitFor returns a channel that will block until the state is achieved.
// Channel is not reusable, function should be called ones per use.
func (g *Gate) WaitFor(state bool) <-chan any {
	g.mu.Lock()
	defer g.mu.Unlock()

	ch := make(chan any, 1)
	if g.state == state {
		ch <- nil
		return ch
	}
	g.waiters = append(g.waiters, ch)
	return ch
}
