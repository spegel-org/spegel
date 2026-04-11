package routing

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
)

var _ Router = &MemoryRouter{}

type MemoryRouter struct {
	resolver map[string][]Peer
	self     Peer
	ready    atomic.Bool
	mx       sync.RWMutex
}

func NewMemoryRouter(resolver map[string][]Peer, self Peer) *MemoryRouter {
	r := &MemoryRouter{
		resolver: resolver,
		self:     self,
	}
	r.ready.Store(true)
	return r
}

func (m *MemoryRouter) SetReadiness(ready bool) {
	m.ready.Store(ready)
}

func (m *MemoryRouter) Ready(ctx context.Context) (bool, error) {
	return m.ready.Load(), nil
}

func (m *MemoryRouter) Lookup(ctx context.Context, key string, count int) (*Iterator, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	iterator := NewIterator()
	peers, ok := m.resolver[key]
	if ok {
		for _, peer := range peers {
			iterator.Add(peer)
		}
	}
	iterator.Close()
	return iterator, nil
}

func (m *MemoryRouter) Advertise(ctx context.Context, keys []string) error {
	for _, key := range keys {
		m.Add(key, m.self)
	}
	return nil
}

func (m *MemoryRouter) Withdraw(ctx context.Context, keys []string) error {
	for _, key := range keys {
		m.Delete(key, m.self)
	}
	return nil
}

func (m *MemoryRouter) Add(key string, peer Peer) {
	m.mx.Lock()
	defer m.mx.Unlock()

	peers, ok := m.resolver[key]
	if !ok {
		m.resolver[key] = []Peer{peer}
		return
	}
	for _, p := range peers {
		if p.Host == peer.Host {
			return
		}
	}
	m.resolver[key] = append(peers, peer)
}

func (m *MemoryRouter) Delete(key string, peer Peer) {
	m.mx.Lock()
	defer m.mx.Unlock()

	peers, ok := m.resolver[key]
	if !ok {
		return
	}
	peers = slices.DeleteFunc(peers, func(v Peer) bool {
		return v.Host == peer.Host
	})
	m.resolver[key] = peers
}

func (m *MemoryRouter) Get(key string) ([]Peer, bool) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	peers, ok := m.resolver[key]
	return peers, ok
}
