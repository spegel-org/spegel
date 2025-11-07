package channel

import "sync"

// Gate implements a syncrhonization mechanism that enables opening and closing a blocking channel.
type Gate struct {
	ch   chan any
	mu   sync.RWMutex
	open bool
}

// NewGate returns a new gate that is closed.
func NewGate() *Gate {
	return &Gate{
		ch:   make(chan any),
		open: false,
	}
}

// IsOpen returns true when the gate is open.
func (g *Gate) IsOpen() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.open
}

// Set opens or closes the gate. Will return early if the same state is set.
// When open the gate channel is clsosed, and when closed a new channel is created.
func (g *Gate) Set(open bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.open == open {
		return
	}
	g.open = open

	if g.open {
		close(g.ch)
	} else {
		g.ch = make(chan any)
	}
}

// Wait returns a channel that is closed when the gate is opened.
// Channel is replaced on open so the returned channel should not be reused.
func (g *Gate) Wait() <-chan any {
	return g.ch
}
