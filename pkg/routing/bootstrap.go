package routing

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Bootstrapper resolves peers to bootstrap with for the P2P router.
type Bootstrapper interface {
	// Run starts the bootstrap process. Should be blocking even if not needed.
	Run(ctx context.Context, id string) error
	// Get returns a list of peers that should be used as bootstrap nodes.
	// If the peer ID is empty it will be resolved.
	// If the address is missing a port the P2P router port will be used.
	Get(ctx context.Context) ([]peer.AddrInfo, error)
}

var _ Bootstrapper = &StaticBootstrapper{}

type StaticBootstrapper struct {
	peers []peer.AddrInfo
	mx    sync.RWMutex
}

func NewStaticBootstrapperFromStrings(peerStrs []string) (*StaticBootstrapper, error) {
	peers := []peer.AddrInfo{}
	for _, peerStr := range peerStrs {
		peer, err := peer.AddrInfoFromString(peerStr)
		if err != nil {
			return nil, err
		}
		peers = append(peers, *peer)
	}
	return NewStaticBootstrapper(peers), nil
}

func NewStaticBootstrapper(peers []peer.AddrInfo) *StaticBootstrapper {
	return &StaticBootstrapper{
		peers: peers,
	}
}

func (b *StaticBootstrapper) Run(ctx context.Context, id string) error {
	<-ctx.Done()
	return nil
}

func (b *StaticBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	b.mx.RLock()
	defer b.mx.RUnlock()
	return b.peers, nil
}

func (b *StaticBootstrapper) SetPeers(peers []peer.AddrInfo) {
	b.mx.Lock()
	defer b.mx.Unlock()
	b.peers = peers
}

var _ Bootstrapper = &DNSBootstrapper{}

type DNSBootstrapper struct {
	resolver *net.Resolver
	host     string
	limit    int
}

func NewDNSBootstrapper(host string, limit int) *DNSBootstrapper {
	return &DNSBootstrapper{
		resolver: &net.Resolver{},
		host:     host,
		limit:    limit,
	}
}

func (b *DNSBootstrapper) Run(ctx context.Context, id string) error {
	<-ctx.Done()
	return nil
}

func (b *DNSBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	ips, err := b.resolver.LookupIPAddr(ctx, b.host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, err
	}
	slices.SortFunc(ips, func(a, b net.IPAddr) int {
		return strings.Compare(a.String(), b.String())
	})
	addrInfos := []peer.AddrInfo{}
	for _, ip := range ips {
		addr, err := manet.FromIPAndZone(ip.IP, ip.Zone)
		if err != nil {
			return nil, err
		}
		addrInfos = append(addrInfos, peer.AddrInfo{
			ID:    "",
			Addrs: []ma.Multiaddr{addr},
		})
	}
	limit := min(len(addrInfos), b.limit)
	return addrInfos[:limit], nil
}

var _ Bootstrapper = &HTTPBootstrapper{}

type HTTPBootstrapper struct {
	addr string
	peer string
}

func NewHTTPBootstrapper(addr, peer string) *HTTPBootstrapper {
	return &HTTPBootstrapper{
		addr: addr,
		peer: peer,
	}
}

func (bs *HTTPBootstrapper) Run(ctx context.Context, id string) error {
	g, ctx := errgroup.WithContext(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // ignore
		w.Write([]byte(id))
	})
	srv := http.Server{
		Addr:    bs.addr,
		Handler: mux,
	}
	g.Go(func() error {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}

func (bs *HTTPBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	resp, err := http.DefaultClient.Get(bs.peer)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	addr, err := ma.NewMultiaddr(string(b))
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}
	return []peer.AddrInfo{*addrInfo}, nil
}
