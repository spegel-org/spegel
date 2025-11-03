package routing

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryRouter(t *testing.T) {
	t.Parallel()

	r := NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{})

	isReady, err := r.Ready(t.Context())
	require.NoError(t, err)
	require.True(t, isReady)
	r.SetReadiness(false)
	isReady, err = r.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, isReady)
	r.SetReadiness(true)

	err = r.Advertise(t.Context(), []string{"foo"})
	require.NoError(t, err)
	r.Add("foo", netip.MustParseAddrPort("127.0.0.1:9090"))
	rr, err := r.Lookup(t.Context(), "foo", 2)
	require.NoError(t, err)
	peers := []netip.AddrPort{}
	for range 2 {
		peer, err := rr.Next()
		require.NoError(t, err)
		peers = append(peers, peer)
	}

	require.Len(t, peers, 2)
	peers, ok := r.Get("foo")
	require.True(t, ok)
	require.Len(t, peers, 2)

	rr, err = r.Lookup(t.Context(), "bar", 1)
	require.NoError(t, err)
	_, err = rr.Next()
	require.ErrorIs(t, err, ErrNoNext)
	_, ok = r.Get("bar")
	require.False(t, ok)
}
