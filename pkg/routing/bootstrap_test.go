package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
)

func TestStaticBootstrap(t *testing.T) {
	t.Parallel()

	peers := []peer.AddrInfo{
		{
			ID:    "foo",
			Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
		},
		{
			ID:    "bar",
			Addrs: []ma.Multiaddr{manet.IP6Loopback},
		},
	}
	bs := NewStaticBootstrapper(peers)

	ctx, cancel := context.WithCancel(t.Context())
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return bs.Run(gCtx, "")
	})

	bsPeers, err := bs.Get(t.Context())
	require.NoError(t, err)
	require.ElementsMatch(t, peers, bsPeers)

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}

func TestHTTPBootstrap(t *testing.T) {
	t.Parallel()

	id := "/ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ"
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // ignore
		w.Write([]byte(id))
	}))
	defer svr.Close()

	bs := NewHTTPBootstrapper(":", svr.URL)

	ctx, cancel := context.WithCancel(t.Context())
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return bs.Run(gCtx, "")
	})

	addrInfos, err := bs.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, addrInfos, 1)
	addrInfo := addrInfos[0]
	require.Len(t, addrInfo.Addrs, 1)
	require.Equal(t, "/ip4/104.131.131.82/tcp/4001", addrInfo.Addrs[0].String())
	require.Equal(t, "QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ", addrInfo.ID.String())

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}
