package routing

import (
	"context"
	"net/netip"
	"sync"
)

type MockRouter struct {
	resolver map[string][]netip.AddrPort
	self     netip.AddrPort
	mx       sync.RWMutex
}

func NewMockRouter(resolver map[string][]netip.AddrPort, self netip.AddrPort) *MockRouter {
	return &MockRouter{
		resolver: resolver,
		self:     self,
	}
}

func (m *MockRouter) Ready() (bool, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()
	return len(m.resolver) > 0, nil
}

func (m *MockRouter) Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan netip.AddrPort, error) {
	peerCh := make(chan netip.AddrPort, count)
	peers, ok := m.resolver[key]
	// If not peers exist close the channel to stop any consumer.
	if !ok {
		close(peerCh)
		return peerCh, nil
	}
	go func() {
		m.mx.RLock()
		defer m.mx.RUnlock()
		for _, peer := range peers {
			peerCh <- peer
		}
		close(peerCh)
	}()
	return peerCh, nil
}

func (m *MockRouter) Advertise(ctx context.Context, keys []string) error {
	m.mx.Lock()
	defer m.mx.Unlock()
	for _, key := range keys {
		m.resolver[key] = []netip.AddrPort{m.self}
	}
	return nil
}

func (m *MockRouter) LookupKey(key string) ([]netip.AddrPort, bool) {
	m.mx.RLock()
	defer m.mx.RUnlock()
	v, ok := m.resolver[key]
	return v, ok
}
