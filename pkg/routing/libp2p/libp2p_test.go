package libp2p

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/go-openapi/testify/v2/assert"
	"github.com/go-openapi/testify/v2/require"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/kvick-org/pkg/errgroup"
	"github.com/kvick-org/pkg/option"

	"github.com/spegel-org/spegel/pkg/routing"
)

func TestRaceBootstrapPeers(t *testing.T) {
	t.Parallel()

	t.Run("empty peer list returns without connecting", func(t *testing.T) {
		called := false
		err := raceBootstrapPeers(t.Context(), nil, func(context.Context, peer.AddrInfo) error {
			called = true
			return nil
		})

		require.NoError(t, err)
		require.False(t, called)
	})

	t.Run("hedges after delay and cancels pending peers when a peer succeeds", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			started := make(chan peer.ID, 3)
			canceled := make(chan peer.ID, 2)
			done := make(chan error, 1)

			go func() {
				done <- raceBootstrapPeers(t.Context(), []peer.AddrInfo{{ID: "first"}, {ID: "second"}, {ID: "third"}}, func(ctx context.Context, addrInfo peer.AddrInfo) error {
					started <- addrInfo.ID
					if addrInfo.ID != "third" {
						<-ctx.Done()
						canceled <- addrInfo.ID
						return ctx.Err()
					}
					return nil
				})
			}()

			initialStarts := map[peer.ID]struct{}{<-started: {}, <-started: {}}
			assert.MapContainsT(t, initialStarts, "first")
			assert.MapContainsT(t, initialStarts, "second")

			// The initial batch has started; verify the next peer is not launched before the delay.
			synctest.Sleep(bootstrapPeerHedgeDelay - time.Nanosecond)
			require.Empty(t, started)

			// The third peer should start after the delay.
			synctest.Sleep(time.Nanosecond)
			require.Len(t, started, 1)
			require.EqualT(t, peer.ID("third"), <-started)

			require.NoError(t, <-done)
			synctest.Wait()
			require.Len(t, canceled, 2)
		})
	})

	t.Run("first peer succeeds after second peer fails", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			err := raceBootstrapPeers(t.Context(), []peer.AddrInfo{{ID: "first"}, {ID: "second"}}, func(ctx context.Context, addrInfo peer.AddrInfo) error {
				if addrInfo.ID == "second" {
					return errors.New("connection refused")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(4 * time.Second):
					return nil
				}
			})
			require.NoError(t, err)
			require.EqualT(t, 4*time.Second, time.Since(start))
		})
	})

	t.Run("failures try next peer immediately and are returned", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			attempts := make(chan peer.ID, 4)
			peerErrors := map[peer.ID]error{
				"first":  errors.New("first peer failed"),
				"second": errors.New("second peer failed"),
				"third":  errors.New("third peer failed"),
				"fourth": errors.New("fourth peer failed"),
			}
			peers := []peer.AddrInfo{{ID: "first"}, {ID: "second"}, {ID: "third"}, {ID: "fourth"}}
			err := raceBootstrapPeers(t.Context(), peers, func(ctx context.Context, addrInfo peer.AddrInfo) error {
				attempts <- addrInfo.ID
				return peerErrors[addrInfo.ID]
			})
			started := map[peer.ID]struct{}{}
			for range peers {
				started[<-attempts] = struct{}{}
			}
			for id, peerErr := range peerErrors {
				require.MapContainsT(t, started, id)
				require.ErrorIs(t, err, peerErr)
			}
			synctest.Wait()
			require.EqualT(t, time.Duration(0), time.Since(start))
		})
	})

	t.Run("deadline cancels pending peers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			canceled := make(chan peer.ID, 2)
			err := raceBootstrapPeers(ctx, []peer.AddrInfo{{ID: "first"}, {ID: "second"}}, func(ctx context.Context, addrInfo peer.AddrInfo) error {
				<-ctx.Done()
				canceled <- addrInfo.ID
				return ctx.Err()
			})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			synctest.Wait()
			require.Len(t, canceled, 2)
		})
	})
}

func TestP2PRouterOptions(t *testing.T) {
	t.Parallel()

	libp2pOpts := []libp2p.Option{
		libp2p.ListenAddrStrings("foo"),
	}
	opts := []RouterOption{
		WithLibP2POptions(libp2pOpts...),
		WithDataDir("foobar"),
	}
	cfg := RouterConfig{}
	err := option.Apply(&cfg, opts...)
	require.NoError(t, err)
	require.Equal(t, libp2pOpts, cfg.Libp2pOpts)
	require.EqualT(t, "foobar", cfg.DataDir)
}

func TestP2PRouter(t *testing.T) {
	t.Parallel()

	log := tlog.NewTestLogger(t)
	ctx := logr.NewContext(t.Context(), log)
	ctx, cancel := context.WithCancel(ctx)
	group := errgroup.WithContext(ctx)

	// Create primary router with no peer to bootstrap with.
	primaryBs := NewStaticBootstrapper(nil)
	primaryRouter, err := NewRouter(t.Context(), "localhost:0", primaryBs, routing.PeerMetadata{RegistryPort: 9090})
	require.NoError(t, err)
	group.Go(func(ctx context.Context) error {
		return primaryRouter.Run(ctx)
	})
	ready, err := primaryRouter.Ready(t.Context())
	require.NoError(t, err)
	require.FalseT(t, ready)

	// Advertise and Withdraw nil keys should not error.
	err = primaryRouter.Advertise(t.Context(), nil)
	require.NoError(t, err)
	err = primaryRouter.Withdraw(t.Context(), nil)
	require.NoError(t, err)

	// Advertising while offline should not error.
	advertisedKey := "will find key"
	err = primaryRouter.Advertise(t.Context(), []string{advertisedKey})
	require.NoError(t, err)

	// Provider store should contain self.
	c, err := createCid(advertisedKey)
	require.NoError(t, err)
	addrInfos, err := primaryRouter.kdht.FindProviders(t.Context(), c)
	require.NoError(t, err)
	require.Len(t, addrInfos, 1)
	require.EqualT(t, primaryRouter.host.ID(), addrInfos[0].ID)

	// Lookup local key should not return self when offline.
	iter, err := primaryRouter.Lookup(t.Context(), advertisedKey, 3)
	require.NoError(t, err)
	<-iter.Exhausted()
	_, ok := iter.Acquire()
	require.FalseT(t, ok)

	// Create routers that all bootstrap with the primary router.
	routers := []*Router{}
	for range 30 {
		bs := NewStaticBootstrapper([]peer.AddrInfo{*host.InfoFromHost(primaryRouter.host)})
		r, err := NewRouter(t.Context(), "localhost:0", bs, routing.PeerMetadata{RegistryPort: 9091})
		require.NoError(t, err)
		group.Go(func(ctx context.Context) error {
			return r.Run(ctx)
		})
		routers = append(routers, r)
	}

	// All routers should eventually be ready as bootstrap has happened.
	<-primaryRouter.connectivityGate.WaitFor(false)
	ready, err = primaryRouter.Ready(t.Context())
	require.NoError(t, err)
	require.TrueT(t, ready)
	require.EventuallyWith(t, func(c *assert.CollectT) {
		stats, err := primaryRouter.prov.Stats(t.Context())
		require.NoError(c, err)
		require.EqualT(c, int64(1), stats.Operations.Past.KeysProvided)
	}, 5*time.Second, time.Second)

	for _, router := range routers {
		<-router.connectivityGate.WaitFor(false)
		ready, err := router.Ready(t.Context())
		require.NoError(t, err)
		require.TrueT(t, ready)
	}
	require.EqualT(t, 30, primaryRouter.kdht.RoutingTable().Size())

	// Lookup should not return self when online.
	iter, err = primaryRouter.Lookup(t.Context(), advertisedKey, 3)
	require.NoError(t, err)
	<-iter.Exhausted()
	_, ok = iter.Acquire()
	require.FalseT(t, ok)

	// Advertised keys should be found.
	for _, r := range routers {
		iter, err = r.Lookup(t.Context(), advertisedKey, 3)
		require.NoError(t, err)
		<-iter.Ready()
		peer, ok := iter.Acquire()
		require.TrueT(t, ok)
		iter.Release(peer)
		require.EqualT(t, primaryRouter.host.ID().String(), peer.Host)
		primaryIPAddrs, err := toIPAddrs(primaryRouter.host.Addrs())
		require.NoError(t, err)
		require.ElementsMatch(t, primaryIPAddrs, peer.Addresses)

		iter, err = r.Lookup(t.Context(), "wont find key", 3)
		require.NoError(t, err)
		<-iter.Exhausted()
		_, ok = iter.Acquire()
		require.FalseT(t, ok)
	}

	// Advertise key from another router and lookup.
	newKey := "new"
	lastRouter := routers[len(routers)-1]
	err = lastRouter.Advertise(t.Context(), []string{newKey})
	require.NoError(t, err)

	require.EventuallyWith(t, func(c *assert.CollectT) {
		stats, err := lastRouter.prov.Stats(t.Context())
		require.NoError(c, err)
		require.EqualT(c, int64(1), stats.Operations.Past.KeysProvided)
	}, 5*time.Second, 1*time.Second)

	iter, err = primaryRouter.Lookup(t.Context(), newKey, 3)
	require.NoError(t, err)
	<-iter.Ready()
	peer, ok := iter.Acquire()
	require.TrueT(t, ok)
	iter.Release(peer)
	require.EqualT(t, lastRouter.host.ID().String(), peer.Host)
	lastIPAddrs, err := toIPAddrs(lastRouter.host.Addrs())
	require.NoError(t, err)
	require.ElementsMatch(t, lastIPAddrs, peer.Addresses)

	// Shutdown should complete without errors.
	cancel()
	err = group.Wait()
	require.NoError(t, err)
}

func TestProvideTTL(t *testing.T) {
	t.Parallel()

	runCtx, runCancel := context.WithCancel(t.Context())
	group := errgroup.WithContext(runCtx)

	routers := []*Router{}
	for range 5 {
		bs := NewStaticBootstrapper(nil)
		router, err := NewRouter(t.Context(), ":0", bs, routing.PeerMetadata{RegistryPort: 9090}, WithAdvertiseTTL(5*time.Second), WithMaxReprovideDelay(1*time.Second))
		require.NoError(t, err)
		group.Go(func(ctx context.Context) error {
			return router.Run(ctx)
		})
		for _, r := range routers {
			bs.Add(*host.InfoFromHost(r.host))
		}
		routers = append(routers, router)
	}

	// Advertise a key that is expected to expire.
	err := routers[0].Advertise(t.Context(), []string{"foo"})
	require.NoError(t, err)
	require.EventuallyWith(t, func(c *assert.CollectT) {
		stats, err := routers[0].prov.Stats(t.Context())
		require.NoError(c, err)
		require.EqualT(c, int64(1), stats.Operations.Past.KeysProvided)
	}, 5*time.Second, time.Second)
	err = routers[0].Withdraw(t.Context(), []string{"foo"})
	require.NoError(t, err)

	// Do lookup immediately which should be found.
	for _, router := range routers[1:] {
		iter, err := router.Lookup(t.Context(), "foo", 1)
		require.NoError(t, err)
		<-iter.Ready()
		_, ok := iter.Acquire()
		require.TrueT(t, ok)
	}

	// Wait past TTL and expect content to not be found.
	time.Sleep(10 * time.Second)

	for _, router := range routers[1:] {
		iter, err := router.Lookup(t.Context(), "foo", 1)
		require.NoError(t, err)
		<-iter.Exhausted()
		_, ok := iter.Acquire()
		require.FalseT(t, ok)
	}

	runCancel()
	err = group.Wait()
	require.NoError(t, err)
}

func TestListenMultiaddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		addr                string
		expectedListenAddrs []ma.Multiaddr
	}{
		{
			name: "listen address type not specified",
			addr: ":9090",
			expectedListenAddrs: []ma.Multiaddr{
				ma.StringCast("/ip6/::/udp/9090/quic-v1"),
				ma.StringCast("/ip6/::/tcp/9090"),
				ma.StringCast("/ip4/0.0.0.0/udp/9090/quic-v1"),
				ma.StringCast("/ip4/0.0.0.0/tcp/9090"),
			},
		},
		{
			name: "ipv4 only",
			addr: "192.168.1.24:7892",
			expectedListenAddrs: []ma.Multiaddr{
				ma.StringCast("/ip4/192.168.1.24/udp/7892/quic-v1"),
				ma.StringCast("/ip4/192.168.1.24/tcp/7892"),
			},
		},
		{
			name: "ipv6 only",
			addr: "[::]:9090",
			expectedListenAddrs: []ma.Multiaddr{
				ma.StringCast("/ip6/::/udp/9090/quic-v1"),
				ma.StringCast("/ip6/::/tcp/9090"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listenAddrs, err := listenMultiaddrs(tt.addr)
			require.NoError(t, err)
			for i, e := range tt.expectedListenAddrs {
				require.EqualT(t, e.String(), listenAddrs[i].String())
			}
		})
	}
}

func TestCreateCid(t *testing.T) {
	t.Parallel()

	c, err := createCid("foobar")
	require.NoError(t, err)
	require.EqualT(t, "bafkreigdvoh7cnza5cwzar65hfdgwpejotszfqx2ha6uuolaofgk54ge6i", c.String())
}

func TestAddrsEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a        []ma.Multiaddr
		b        []ma.Multiaddr
		expected bool
	}{
		{
			name:     "empty addresses",
			a:        []ma.Multiaddr{},
			b:        []ma.Multiaddr{},
			expected: false,
		},
		{
			name:     "IP4 match",
			a:        []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			b:        []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			expected: true,
		},
		{
			name:     "IP4 do not match",
			a:        []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			b:        []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.2")},
			expected: false,
		},
		{
			name: "IP6 match",
			a:    []ma.Multiaddr{ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba")},
			b: []ma.Multiaddr{
				ma.StringCast("/ip4/192.168.1.1"),
				ma.StringCast("/ip6/c3c9:152b:73d1:dad0:e2f9:a521:6356:88ba"),
			},
			expected: true,
		},
		{
			name: "non IP address",
			a: []ma.Multiaddr{
				ma.StringCast("/tcp/5000"),
				ma.StringCast("/ip4/192.168.1.1"),
			},
			b:        []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.1")},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches := addrsEqual(tt.a, tt.b)
			require.EqualT(t, tt.expected, matches)
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
	require.TrueT(t, ok)
	require.TrueT(t, firstPrivKey.Equals(secondPrivKey))
}

func TestListPeers(t *testing.T) {
	t.Parallel()

	isolatedBs := NewStaticBootstrapper(nil)
	isolatedRouter, err := NewRouter(t.Context(), "localhost:0", isolatedBs, routing.PeerMetadata{RegistryPort: 9090})
	require.NoError(t, err)

	isolatedAddrs, err := isolatedRouter.ListPeers()
	require.NoError(t, err)
	require.Empty(t, isolatedAddrs)
	require.NotNil(t, isolatedAddrs)
}

func TestLocalAddress(t *testing.T) {
	t.Parallel()

	bs := NewStaticBootstrapper(nil)
	router, err := NewRouter(t.Context(), ":0", bs, routing.PeerMetadata{RegistryPort: 9090})
	require.NoError(t, err)

	ipAddrs, err := router.LocalAddresses()
	require.NoError(t, err)
	require.NotEmpty(t, ipAddrs)
}

func TestNewResourceManagerSharedSubnet(t *testing.T) {
	t.Parallel()

	rm, err := newResourceManager()
	require.NoError(t, err)
	defer rm.Close()

	// Thirty peers in one IPv6 /56 connecting at once exceeds both of the
	// go-libp2p defaults, 8 concurrent connections and a burst of 16 new ones.
	for i := range 30 {
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip6/2001:db8:0:%x::1/tcp/5001", i))
		require.NoError(t, err)
		scope, err := rm.OpenConnection(network.DirInbound, false, addr)
		require.NoError(t, err)
		t.Cleanup(scope.Done)
	}
}
