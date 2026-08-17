package routing

import (
	"context"
	"net/netip"
	"testing"
	"testing/synctest"

	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"

	"github.com/kvick-org/pkg/errgroup"

	"github.com/spegel-org/spegel/pkg/store"
)

var _ store.Watcher = &watcher{}

type watcher struct {
	eventCh chan store.Event
	initial []store.Event
}

func (w *watcher) Add(ctx context.Context, event store.Event) {
	select {
	case <-ctx.Done():
	case w.eventCh <- event:
	}
}

func (w *watcher) Watch(ctx context.Context) ([]store.Event, <-chan store.Event, error) {
	return w.initial, w.eventCh, nil
}

func TestSync(t *testing.T) {
	t.Parallel()

	initial := []store.Event{
		{
			Type:   store.CreateEvent,
			Digest: digest.FromString("foo"),
		},
		{
			Type:   store.CreateEvent,
			Digest: digest.FromString("hello"),
		},
	}
	incoming := []store.Event{
		{
			Type:   store.CreateEvent,
			Digest: digest.FromString("first"),
		},
		{
			Type:   store.CreateEvent,
			Digest: digest.FromString("second"),
		},
		{
			Type:   store.DeleteEvent,
			Digest: digest.FromString("foo"),
		},
	}

	synctest.Test(t, func(t *testing.T) {
		watcher := &watcher{
			initial: initial,
			eventCh: make(chan store.Event),
		}

		self := Peer{
			Host:      "test",
			Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
			Metadata: PeerMetadata{
				RegistryPort: 5000,
			},
		}
		router := NewMemoryRouter(map[string][]Peer{}, self)

		ctx, cancel := context.WithCancel(t.Context())
		group := errgroup.WithContext(ctx)
		group.Go(func(ctx context.Context) error {
			return Sync(ctx, router, watcher)
		})

		// Initial events should be advertised.
		synctest.Wait()
		for _, event := range initial {
			peers, ok := router.Get(event.Digest.String())
			require.TrueT(t, ok)
			require.Len(t, peers, 1)
		}

		// Incoming events should be advertised.
		for _, event := range incoming {
			watcher.Add(t.Context(), event)
		}
		synctest.Wait()
		for _, event := range incoming[:2] {
			peers, ok := router.Get(event.Digest.String())
			require.TrueT(t, ok)
			require.Len(t, peers, 1)
		}
		_, ok := router.Get(incoming[2].Digest.String())
		require.FalseT(t, ok)

		cancel()
		err := group.Wait()
		require.ErrorIs(t, err, context.Canceled)
	})
}
