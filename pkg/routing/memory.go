package routing

import (
	"context"
	"net/netip"
	"slices"
	"sync"
)

var _ Router = &MemoryRouter{}

type MemoryRouter struct {
	resolver map[string][]netip.AddrPort
	self     netip.AddrPort
	mx       sync.RWMutex
}

func NewMemoryRouter(resolver map[string][]netip.AddrPort, self netip.AddrPort) *MemoryRouter {
	return &MemoryRouter{
		resolver: resolver,
		self:     self,
	}
}

func (m *MemoryRouter) Ready(ctx context.Context) (bool, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	return len(m.resolver) > 0, nil
}

func (m *MemoryRouter) Resolve(ctx context.Context, key string, count int) (<-chan netip.AddrPort, error) {
	m.mx.RLock()
	peers, ok := m.resolver[key]
	m.mx.RUnlock()

	peerCh := make(chan netip.AddrPort, count)
	// If no peers exist close the channel to stop any consumer.
	if !ok {
		close(peerCh)
		return peerCh, nil
	}
	go func() {
		for _, peer := range peers {
			peerCh <- peer
		}
		close(peerCh)
	}()
	return peerCh, nil
}

func (m *MemoryRouter) Advertise(ctx context.Context, keys []string) error {
	for _, key := range keys {
		m.Add(key, m.self)
	}
	return nil
}

func (m *MemoryRouter) Add(key string, ap netip.AddrPort) {
	m.mx.Lock()
	defer m.mx.Unlock()

	v, ok := m.resolver[key]
	if !ok {
		m.resolver[key] = []netip.AddrPort{ap}
		return
	}
	if slices.Contains(v, ap) {
		return
	}
	m.resolver[key] = append(v, ap)
}

func (m *MemoryRouter) Lookup(key string) ([]netip.AddrPort, bool) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	v, ok := m.resolver[key]
	return v, ok
}
