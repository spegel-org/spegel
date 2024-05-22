package routing

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryRouter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{})

	isReady, err := r.Ready(ctx)
	require.NoError(t, err)
	require.False(t, isReady)
	err = r.Advertise(ctx, []string{"foo"})
	require.NoError(t, err)
	isReady, err = r.Ready(ctx)
	require.NoError(t, err)
	require.True(t, isReady)

	r.Add("foo", netip.MustParseAddrPort("127.0.0.1:9090"))
	peerCh, err := r.Resolve(ctx, "foo", true, 2)
	require.NoError(t, err)
	peers := []netip.AddrPort{}
	for peer := range peerCh {
		peers = append(peers, peer)
	}
	require.Len(t, peers, 2)
}
