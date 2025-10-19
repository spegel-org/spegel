package routing

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
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

	firstBs := NewStaticBootstrapper(nil)
	firstRouter, err := NewP2PRouter(ctx, "localhost:0", firstBs, "9090")
	require.NoError(t, err)
	g.Go(func() error {
		return firstRouter.Run(gCtx)
	})

	// Router will never be ready because it cant bootstrap.
	ready, err := firstRouter.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, ready)

	// Start new router and verify readiness.
	secondBs := NewStaticBootstrapper([]peer.AddrInfo{*host.InfoFromHost(firstRouter.host)})
	secondRouter, err := NewP2PRouter(ctx, "localhost:0", secondBs, "9090")
	require.NoError(t, err)
	g.Go(func() error {
		return secondRouter.Run(gCtx)
	})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		// First router should be ready because second connected.
		ready, err = firstRouter.Ready(t.Context())
		require.NoError(c, err)
		require.True(c, ready)

		// Second router should be ready because it bootstrapped.
		ready, err = secondRouter.Ready(t.Context())
		require.NoError(c, err)
		require.True(c, ready)
	}, 10*time.Second, time.Second)

	// Advertise nil keys.
	err = firstRouter.Advertise(ctx, nil)
	require.NoError(t, err)

	// Lookup key that does not exist.
	rr, err := firstRouter.Lookup(ctx, "foo", 1)
	require.NoError(t, err)
	_, err = rr.Next()
	require.ErrorIs(t, err, ErrNoNext)

	// Advertise and then resolve the key.
	err = firstRouter.Advertise(ctx, []string{"foo"})
	require.NoError(t, err)
	rr, err = firstRouter.Lookup(ctx, "foo", 1)
	require.NoError(t, err)
	peer, err := rr.Next()
	require.NoError(t, err)
	require.True(t, peer.IsValid())

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
