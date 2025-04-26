package routing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestP2PRouterOptions(t *testing.T) {
	t.Parallel()

	libp2pOpts := []libp2p.Option{
		libp2p.ListenAddrStrings("foo"),
	}
	opts := []P2PRouterOption{
		WithLibP2POptions(libp2pOpts...),
		WithDataDir("foobar"),
	}
	cfg := P2PRouterConfig{}
	err := cfg.Apply(opts...)
	require.NoError(t, err)
	require.Equal(t, libp2pOpts, cfg.Libp2pOpts)
	require.Equal(t, "foobar", cfg.DataDir)
}

func TestP2PRouter(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	bs := NewStaticBootstrapper(nil)
	router, err := NewP2PRouter(ctx, "localhost:0", bs, "9090")
	require.NoError(t, err)

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return router.Run(gCtx)
	})

	// TODO (phillebaba): There is a test flake that sometime occurs sometimes if code runs too fast.
	// Flake results in a peer being returned without an address. Revisit in Go 1.24 to see if this can be solved better.
	time.Sleep(1 * time.Second)

	err = router.Advertise(ctx, nil)
	require.NoError(t, err)
	peerCh, err := router.Resolve(ctx, "foo", 1)
	require.NoError(t, err)
	peer := <-peerCh
	require.False(t, peer.IsValid())

	err = router.Advertise(ctx, []string{"foo"})
	require.NoError(t, err)
	peerCh, err = router.Resolve(ctx, "foo", 1)
	require.NoError(t, err)
	peer = <-peerCh
	require.True(t, peer.IsValid())

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}

func TestReady(t *testing.T) {
	t.Parallel()

	bs := NewStaticBootstrapper(nil)
	router, err := NewP2PRouter(t.Context(), "localhost:0", bs, "9090")
	require.NoError(t, err)

	// Should not be ready if no peers are found.
	isReady, err := router.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, isReady)

	// Should be ready if only peer is host.
	bs.SetPeers([]peer.AddrInfo{*host.InfoFromHost(router.host)})
	isReady, err = router.Ready(t.Context())
	require.NoError(t, err)
	require.True(t, isReady)

	// Shouldd be not ready with multiple peers but empty routing table.
	bs.SetPeers([]peer.AddrInfo{{}, {}})
	isReady, err = router.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, isReady)

	// Should be ready with multiple peers and populated routing table.
	newPeer, err := router.kdht.RoutingTable().GenRandPeerID(0)
	require.NoError(t, err)
	ok, err := router.kdht.RoutingTable().TryAddPeer(newPeer, false, false)
	require.NoError(t, err)
	require.True(t, ok)
	bs.SetPeers([]peer.AddrInfo{{}, {}})
	isReady, err = router.Ready(t.Context())
	require.NoError(t, err)
	require.True(t, isReady)
}

func TestBootstrapFunc(t *testing.T) {
	t.Parallel()

	log := tlog.NewTestLogger(t)
	ctx := logr.NewContext(t.Context(), log)

	mn, err := mocknet.WithNPeers(2)
	require.NoError(t, err)

	tests := []struct {
		name     string
		peers    []peer.AddrInfo
		expected []string
	}{
		{
			name:     "no peers",
			peers:    []peer.AddrInfo{},
			expected: []string{},
		},
		{
			name: "nothing missing",
			peers: []peer.AddrInfo{
				{
					ID:    "foo",
					Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1/tcp/8080")},
				},
			},
			expected: []string{"/ip4/192.168.1.1/tcp/8080/p2p/foo"},
		},
		{
			name: "only self",
			peers: []peer.AddrInfo{
				{
					ID:    mn.Hosts()[0].ID(),
					Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1/tcp/8080")},
				},
			},
			expected: []string{},
		},
		{
			name: "missing port",
			peers: []peer.AddrInfo{
				{
					ID:    "foo",
					Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
				},
			},
			expected: []string{"/ip4/192.168.1.1/tcp/4242/p2p/foo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bs := NewStaticBootstrapper(tt.peers)
			f := bootstrapFunc(ctx, bs, mn.Hosts()[0])
			peers := f()

			peerStrs := []string{}
			for _, p := range peers {
				id, err := p.ID.Marshal()
				require.NoError(t, err)
				peerStrs = append(peerStrs, fmt.Sprintf("%s/p2p/%s", p.Addrs[0].String(), string(id)))
			}
			require.ElementsMatch(t, tt.expected, peerStrs)
		})
	}
}

func TestListenMultiaddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		expected []string
	}{
		{
			name:     "listen address type not specified",
			addr:     ":9090",
			expected: []string{"/ip6/::/tcp/9090", "/ip4/0.0.0.0/tcp/9090"},
		},
		{
			name:     "ipv4 only",
			addr:     "0.0.0.0:9090",
			expected: []string{"/ip4/0.0.0.0/tcp/9090"},
		},
		{
			name:     "ipv6 only",
			addr:     "[::]:9090",
			expected: []string{"/ip6/::/tcp/9090"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			multiAddrs, err := listenMultiaddrs(tt.addr)
			require.NoError(t, err)
			//nolint: testifylint // This is easier to read and understand.
			require.Equal(t, len(tt.expected), len(multiAddrs))
			for i, e := range tt.expected {
				require.Equal(t, e, multiAddrs[i].String())
			}
		})
	}
}

func TestIsIp6(t *testing.T) {
	t.Parallel()

	m, err := ma.NewMultiaddr("/ip6/::")
	require.NoError(t, err)
	require.True(t, isIp6(m))
	m, err = ma.NewMultiaddr("/ip4/0.0.0.0")
	require.NoError(t, err)
	require.False(t, isIp6(m))
}

func TestCreateCid(t *testing.T) {
	t.Parallel()

	c, err := createCid("foobar")
	require.NoError(t, err)
	require.Equal(t, "bafkreigdvoh7cnza5cwzar65hfdgwpejotszfqx2ha6uuolaofgk54ge6i", c.String())
}

func TestHostMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		host     peer.AddrInfo
		addrInfo peer.AddrInfo
		expected bool
	}{
		{
			name: "ID match",
			host: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			addrInfo: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			expected: true,
		},
		{
			name: "ID do not match",
			host: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			addrInfo: peer.AddrInfo{
				ID:    "bar",
				Addrs: []ma.Multiaddr{},
			},
			expected: false,
		},
		{
			name: "IP4 match",
			host: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			addrInfo: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			expected: true,
		},
		{
			name: "IP4 do not match",
			host: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			addrInfo: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.2")},
			},
			expected: false,
		},
		{
			name: "IP6 match",
			host: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba")},
			},
			addrInfo: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba")},
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches, err := hostMatches(tt.host, tt.addrInfo)
			require.NoError(t, err)
			require.Equal(t, tt.expected, matches)
		})
	}
}

func TestLoadOrCreatePrivateKey(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	data := []byte("hello world")

	firstPrivKey, err := loadOrCreatePrivateKey(t.Context(), tmpDir)
	require.NoError(t, err)
	sig, err := firstPrivKey.Sign(data)
	require.NoError(t, err)
	secondPrivKey, err := loadOrCreatePrivateKey(t.Context(), tmpDir)
	require.NoError(t, err)
	ok, err := secondPrivKey.GetPublic().Verify(data, sig)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, firstPrivKey.Equals(secondPrivKey))
}
