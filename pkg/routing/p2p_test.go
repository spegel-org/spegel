package routing

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/spegel-org/spegel/internal/option"
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
	err := option.Apply(&cfg, opts...)
	require.NoError(t, err)
	require.Equal(t, libp2pOpts, cfg.Libp2pOpts)
	require.Equal(t, "foobar", cfg.DataDir)
}

func TestP2PRouter(t *testing.T) {
	t.Parallel()

	log := tlog.NewTestLogger(t)
	ctx := logr.NewContext(t.Context(), log)
	ctx, cancel := context.WithCancel(ctx)
	g, gCtx := errgroup.WithContext(ctx)

	// Remove the 8 connection per IP limit.
	routerOpts := []P2PRouterOption{
		WithLibP2POptions(libp2p.ResourceManager(&network.NullResourceManager{})),
	}

	// Create primary router with no peer to bootstrap with.
	primaryBs := NewStaticBootstrapper(nil)
	primaryRouter, err := NewP2PRouter(t.Context(), "localhost:0", primaryBs, "9090", routerOpts...)
	require.NoError(t, err)
	g.Go(func() error {
		return primaryRouter.Run(gCtx)
	})
	ready, err := primaryRouter.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, ready)
	primaryIP, err := manet.ToIP(primaryRouter.host.Addrs()[0])
	require.NoError(t, err)

	// Advertise and Withdraw nil keys should not error.
	err = primaryRouter.Advertise(t.Context(), nil)
	require.NoError(t, err)
	err = primaryRouter.Withdraw(t.Context(), nil)
	require.NoError(t, err)

	// Advertising while offline should not error.
	advertisedKey := "will find key"
	err = primaryRouter.Advertise(t.Context(), []string{advertisedKey})
	require.NoError(t, err)

	// Lookup local key should not return self.
	bal, err := primaryRouter.Lookup(t.Context(), advertisedKey, 3)
	require.NoError(t, err)
	_, err = bal.Next()
	require.ErrorIs(t, err, ErrNoNext)

	// Create routers that all bootstrap with the primary router.
	routers := []*P2PRouter{}
	for range 30 {
		bs := NewStaticBootstrapper([]peer.AddrInfo{*host.InfoFromHost(primaryRouter.host)})
		r, err := NewP2PRouter(t.Context(), "localhost:0", bs, "9091", routerOpts...)
		require.NoError(t, err)
		g.Go(func() error {
			return r.Run(gCtx)
		})
		routers = append(routers, r)
	}

	// All routers should eventually be ready as bootstrap has happened.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ready, err = primaryRouter.Ready(t.Context())
		require.NoError(c, err)
		require.True(c, ready)
		require.Equal(c, int64(1), primaryRouter.prov.Stats().Operations.Past.KeysProvided, 1)
	}, 5*time.Second, time.Second)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		for _, r := range routers {
			ready, err := r.Ready(t.Context())
			require.NoError(c, err)
			require.True(c, ready)
		}
	}, 5*time.Second, time.Second)
	require.Equal(t, 30, primaryRouter.kdht.RoutingTable().Size())

	// Advertised keys should be found.
	for _, r := range routers {
		bal, err = r.Lookup(t.Context(), advertisedKey, 3)
		require.NoError(t, err)
		addrPort, err := bal.Next()
		require.NoError(t, err)
		require.Equal(t, primaryIP.String(), addrPort.Addr().String())
		require.Equal(t, uint16(9091), addrPort.Port())

		bal, err = r.Lookup(t.Context(), "wont find key", 3)
		require.NoError(t, err)
		_, err = bal.Next()
		require.ErrorIs(t, err, ErrNoNext)
	}

	// Advertise key from another router and lookup.
	newKey := "new"
	lastRouter := routers[len(routers)-1]
	lastIP, err := manet.ToIP(lastRouter.host.Addrs()[0])
	require.NoError(t, err)
	err = lastRouter.Advertise(t.Context(), []string{newKey})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.Equal(c, int64(1), lastRouter.prov.Stats().Operations.Past.KeysProvided, 1)
	}, 5*time.Second, 1*time.Second)

	bal, err = primaryRouter.Lookup(t.Context(), newKey, 3)
	require.NoError(t, err)
	addrPort, err := bal.Next()
	require.NoError(t, err)
	require.Equal(t, lastIP.String(), addrPort.Addr().String())

	// Shutdown should complete without errors.
	cancel()
	err = g.Wait()
	require.NoError(t, err)
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

func TestAddrInfoMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a        peer.AddrInfo
		b        peer.AddrInfo
		expected bool
	}{
		{
			name: "ID match",
			a: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			b: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			expected: true,
		},
		{
			name: "ID do not match",
			a: peer.AddrInfo{
				ID:    "foo",
				Addrs: []ma.Multiaddr{},
			},
			b: peer.AddrInfo{
				ID:    "bar",
				Addrs: []ma.Multiaddr{},
			},
			expected: false,
		},
		{
			name: "IP4 match",
			a: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			b: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			expected: true,
		},
		{
			name: "IP4 do not match",
			a: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			b: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.2")},
			},
			expected: false,
		},
		{
			name: "IP6 match",
			a: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba")},
			},
			b: peer.AddrInfo{
				ID: "",
				Addrs: []ma.Multiaddr{
					ma.StringCast("/ip4/192.168.1.1"),
					ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba"),
				},
			},
			expected: true,
		},
		{
			name: "non IP address",
			a: peer.AddrInfo{
				ID: "",
				Addrs: []ma.Multiaddr{
					ma.StringCast("/tcp/5000"),
					ma.StringCast("/ip4/192.168.1.1"),
				},
			},
			b: peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches := addrInfoMatches(tt.a, tt.b)
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

func TestListPeers(t *testing.T) {
	t.Parallel()

	isolatedBs := NewStaticBootstrapper(nil)
	isolatedRouter, err := NewP2PRouter(t.Context(), "localhost:0", isolatedBs, "9090")
	require.NoError(t, err)

	isolatedAddrs, err := isolatedRouter.ListPeers()
	require.NoError(t, err)
	require.Empty(t, isolatedAddrs)
	require.NotNil(t, isolatedAddrs)
}

func TestLocalAddress(t *testing.T) {
	t.Parallel()

	bs := NewStaticBootstrapper(nil)
	router, err := NewP2PRouter(t.Context(), ":0", bs, "9090")
	require.NoError(t, err)

	localAddr := router.LocalAddress()
	require.NotEmpty(t, localAddr, "LocalAddress should return a non-empty address")

	_, err4 := ma.NewMultiaddr("/ip4/" + localAddr)
	_, err6 := ma.NewMultiaddr("/ip6/" + localAddr)
	require.True(t, err4 == nil || err6 == nil, "LocalAddress should return a valid IP address")
}
