package routing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/spegel-org/spegel/pkg/httpx"
)

// Bootstrapper resolves peers to bootstrap with for the P2P router.
type Bootstrapper interface {
	// Run starts the bootstrap process. Should be blocking even if not needed.
	Run(ctx context.Context, addrInfo peer.AddrInfo) error
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

func (b *StaticBootstrapper) Run(ctx context.Context, addrInfo peer.AddrInfo) error {
	<-ctx.Done()
	return nil
}

func (b *StaticBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	b.mx.RLock()
	defer b.mx.RUnlock()
	return b.peers, nil
}

func (b *StaticBootstrapper) Add(peer peer.AddrInfo) {
	b.mx.Lock()
	defer b.mx.Unlock()
	b.peers = append(b.peers, peer)
}

var _ Bootstrapper = &DNSBootstrapper{}

type DNSBootstrapper struct {
	resolver *net.Resolver
	host     string
}

func NewDNSBootstrapper(host string) *DNSBootstrapper {
	return &DNSBootstrapper{
		resolver: &net.Resolver{},
		host:     host,
	}
}

func (b *DNSBootstrapper) Run(ctx context.Context, addrInfo peer.AddrInfo) error {
	<-ctx.Done()
	return nil
}

func (b *DNSBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	limit := 3
	networks := []string{"ip4", "ip6"}
	errs := []error{}
	addrInfos := []peer.AddrInfo{}
	for _, network := range networks {
		ipAddrs, err := b.resolver.LookupNetIP(ctx, network, b.host)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(ipAddrs) == 0 {
			continue
		}
		slices.SortFunc(ipAddrs, func(a, b netip.Addr) int {
			return a.Compare(b)
		})
		for _, ipAddr := range ipAddrs[:min(len(ipAddrs), limit)] {
			addr, err := manet.FromIPAndZone(ipAddr.AsSlice(), ipAddr.Zone())
			if err != nil {
				return nil, err
			}
			addrInfo := peer.AddrInfo{
				ID:    "",
				Addrs: []ma.Multiaddr{addr},
			}
			addrInfos = append(addrInfos, addrInfo)
		}
	}
	if len(errs) == len(networks) {
		return nil, errors.Join(errs...)
	}
	return addrInfos, nil
}

var _ Bootstrapper = &HTTPBootstrapper{}

// HTTPBootstrapper resolves bootstrap peers over HTTP.
type HTTPBootstrapper struct {
	httpClient *http.Client
	addr       string
	endpoint   string
}

// NewHTTPBootstrapper creates an HTTP bootstrapper.
func NewHTTPBootstrapper(addr string, bootstrapURL url.URL, pool *x509.CertPool, cert *tls.Certificate) (*HTTPBootstrapper, error) {
	transport := httpx.BaseTransport()
	if pool != nil || cert != nil {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}
		if cert != nil {
			tlsConfig.Certificates = []tls.Certificate{*cert}
		}
		transport.TLSClientConfig = tlsConfig
	}
	client := httpx.BaseClient()
	client.Transport = transport

	return &HTTPBootstrapper{
		httpClient: client,
		addr:       addr,
		endpoint:   bootstrapURL.String(),
	}, nil
}

func (bs *HTTPBootstrapper) Run(ctx context.Context, addrInfo peer.AddrInfo) error {
	if bs.addr == "" {
		<-ctx.Done()
		return nil
	}
	b, err := json.Marshal([]peer.AddrInfo{addrInfo})
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // ignore
		w.Write(b)
	})
	srv := &http.Server{
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
	limit := 3
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bs.endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bs.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpx.DrainAndClose(resp.Body)
	err = httpx.CheckResponseStatus(resp, http.StatusOK)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	bootstrapPeerAddrInfos := []BootstrapPeerAddrInfo{}
	err = json.Unmarshal(b, &bootstrapPeerAddrInfos)
	if err != nil {
		return nil, err
	}
	bootstrapPeerAddrInfos = bootstrapPeerAddrInfos[:min(len(bootstrapPeerAddrInfos), limit)]
	addrInfos, err := FromBootstrapPeerAddrInfos(bootstrapPeerAddrInfos)
	if err != nil {
		return nil, err
	}
	return addrInfos, nil
}

// BootstrapPeerAddrInfo mirrors libp2p's peer.AddrInfo JSON shape but allows the ID to be omitted or empty.
// libp2p's peer.AddrInfo.UnmarshalJSON rejects an empty ID, so we unmarshal into this struct and convert via fromBootstrapPeerAddrInfos.
type BootstrapPeerAddrInfo struct {
	ID    string   `json:"ID"`
	Addrs []string `json:"Addrs"`
}

func FromBootstrapPeerAddrInfos(bootstrapPeerAddrInfos []BootstrapPeerAddrInfo) ([]peer.AddrInfo, error) {
	out := make([]peer.AddrInfo, len(bootstrapPeerAddrInfos))
	for i, bootstrapPeerAddrInfo := range bootstrapPeerAddrInfos {
		addrs := make([]ma.Multiaddr, len(bootstrapPeerAddrInfo.Addrs))
		for j, str := range bootstrapPeerAddrInfo.Addrs {
			addr, err := ma.NewMultiaddr(str)
			if err != nil {
				return nil, err
			}
			addrs[j] = addr
		}
		out[i] = peer.AddrInfo{Addrs: addrs}
		if bootstrapPeerAddrInfo.ID != "" {
			id, err := peer.Decode(bootstrapPeerAddrInfo.ID)
			if err != nil {
				return nil, err
			}
			out[i].ID = id
		}
	}
	return out, nil
}
