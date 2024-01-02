package routing

import (
	"context"
	"sync"
)

type MockRouter struct {
	resolver map[string][]string
	mx       sync.RWMutex
}

func NewMockRouter(resolver map[string][]string) *MockRouter {
	return &MockRouter{
		resolver: resolver,
	}
}

func (m *MockRouter) Close() error {
	return nil
}

func (m *MockRouter) HasMirrors() (bool, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()
	return len(m.resolver) > 0, nil
}

func (m *MockRouter) Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan string, error) {
	peerCh := make(chan string, count)
	peers, ok := m.resolver[key]
	// Not found will look forever until timeout.
	if !ok {
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
		m.resolver[key] = []string{"localhost"}
	}
	return nil
}

func (m *MockRouter) LookupKey(key string) ([]string, bool) {
	m.mx.RLock()
	defer m.mx.RUnlock()
	v, ok := m.resolver[key]
	return v, ok
}
