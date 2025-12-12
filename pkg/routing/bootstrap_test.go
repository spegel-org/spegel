package routing

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/miekg/dns"
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
		return bs.Run(gCtx, peer.AddrInfo{})
	})

	bsPeers, err := bs.Get(t.Context())
	require.NoError(t, err)
	require.ElementsMatch(t, peers, bsPeers)

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}

func TestDNSBootstrap(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	g, gCtx := errgroup.WithContext(ctx)

	rr, err := dns.NewRR("example.com. 30 IN A 10.1.2.3")
	require.NoError(t, err)
	mux := dns.NewServeMux()
	mux.Handle("peers", dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		msg := &dns.Msg{}
		msg.SetReply(m)
		msg.Answer = []dns.RR{rr}
		//nolint:errcheck // Ignore.
		w.WriteMsg(msg)
	}))
	//nolint:noctx // Context not important for testing.
	pc, err := net.ListenPacket("udp", ":0")
	require.NoError(t, err)
	srv := &dns.Server{
		PacketConn: pc,
		Handler:    mux,
	}
	g.Go(func() error {
		return srv.ActivateAndServe()
	})
	g.Go(func() error {
		<-gCtx.Done()
		return srv.Shutdown()
	})

	bs := NewDNSBootstrapper("peers")
	bs.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			//nolint:noctx // Context not important for testing.
			return net.Dial("udp", pc.LocalAddr().String())
		},
	}
	g.Go(func() error {
		return bs.Run(gCtx, peer.AddrInfo{})
	})
	addrInfos, err := bs.Get(ctx)
	require.NoError(t, err)
	require.Len(t, addrInfos, 1)
	require.Len(t, addrInfos[0].Addrs, 1)
	require.Equal(t, "{: [/ip4/10.1.2.3]}", addrInfos[0].String())

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}

func TestHTTPBootstrap(t *testing.T) {
	t.Parallel()

	//nolint:noctx // Context not important for testing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	err = ln.Close()
	require.NoError(t, err)
	parentAddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
	require.NoError(t, err)
	id, err := peer.Decode("12D3KooWAsvvigG9jqjMNWMmqXph6BvszxTus6Fg6k5UZda2iKDB")
	require.NoError(t, err)
	parentAddrInfo := peer.AddrInfo{
		ID:    id,
		Addrs: []ma.Multiaddr{parentAddr},
	}
	parentBs := NewHTTPBootstrapper(ln.Addr().String(), "")
	ctx, cancel := context.WithCancel(t.Context())
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return parentBs.Run(gCtx, parentAddrInfo)
	})

	time.Sleep(100 * time.Millisecond)

	childBs := NewHTTPBootstrapper(":", "http://"+ln.Addr().String())
	addrInfos, err := childBs.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, addrInfos, 1)
	require.Len(t, addrInfos[0].Addrs, 1)
	require.Equal(t, parentAddrInfo.ID, addrInfos[0].ID)
	require.Equal(t, "{12D3KooWAsvvigG9jqjMNWMmqXph6BvszxTus6Fg6k5UZda2iKDB: [/ip4/127.0.0.1/tcp/4001]}", addrInfos[0].String())

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}
