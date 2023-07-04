package routing

import (
	"context"
)

type MockRouter struct {
	resolver map[string][]string
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
	return true, nil
}

func (m *MockRouter) Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan string, error) {
	peerCh := make(chan string, count)
	peers, ok := m.resolver[key]
	// Not found will look forever until timeout.
	if !ok {
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

func (m *MockRouter) Advertise(ctx context.Context, keys []string) error {
	return nil
}
