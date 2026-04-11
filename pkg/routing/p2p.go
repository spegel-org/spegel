package routing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/go-logr/logr"
	"github.com/hashicorp/golang-lru/v2/expirable"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-kad-dht/provider"
	"github.com/libp2p/go-libp2p-kad-dht/records"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spegel-org/spegel/internal/channel"
	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/internal/resilient"
	"github.com/spegel-org/spegel/pkg/metrics"
)

const (
	lookupCacheTTL = 5 * time.Second
)

type P2PRouterConfig struct {
	DataDir           string
	Libp2pOpts        []libp2p.Option
	AdvertiseTTL      time.Duration
	MaxReprovideDelay time.Duration
}

type P2PRouterOption = option.Option[P2PRouterConfig]

func WithLibP2POptions(opts ...libp2p.Option) P2PRouterOption {
	return func(cfg *P2PRouterConfig) error {
		cfg.Libp2pOpts = opts
		return nil
	}
}

func WithDataDir(dataDir string) P2PRouterOption {
	return func(cfg *P2PRouterConfig) error {
		cfg.DataDir = dataDir
		return nil
	}
}

func WithAdvertiseTTL(ttl time.Duration) P2PRouterOption {
	return func(cfg *P2PRouterConfig) error {
		cfg.AdvertiseTTL = ttl
		return nil
	}
}

func WithMaxReprovideDelay(delay time.Duration) P2PRouterOption {
	return func(cfg *P2PRouterConfig) error {
		cfg.MaxReprovideDelay = delay
		return nil
	}
}

var _ Router = &P2PRouter{}

type P2PRouter struct {
	bootstrapper     Bootstrapper
	host             host.Host
	kdht             *dht.IpfsDHT
	prov             *provider.SweepingProvider
	lookupGroup      *singleflight.Group
	lookupCache      *expirable.LRU[string, *Iterator]
	connectivityGate *channel.Gate
	protocols        []ma.Multiaddr
	registryPort     uint16
}

func NewP2PRouter(ctx context.Context, addr string, bs Bootstrapper, registryPortStr string, opts ...P2PRouterOption) (*P2PRouter, error) {
	cfg := P2PRouterConfig{
		AdvertiseTTL:      15 * time.Minute,
		MaxReprovideDelay: 2 * time.Minute,
	}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}

	registryPort, err := strconv.ParseUint(registryPortStr, 10, 16)
	if err != nil {
		return nil, err
	}

	listenAddrs, err := listenMultiaddrs(addr)
	if err != nil {
		return nil, err
	}
	hostOpts := []libp2p.Option{
		libp2p.ChainOptions(
			libp2p.NoTransports,
			libp2p.Transport(quic.NewTransport),
			libp2p.Transport(tcp.NewTCPTransport),
		),
		libp2p.ListenAddrs(listenAddrs...),
		libp2p.DisableIdentifyAddressDiscovery(),
		libp2p.PrometheusRegisterer(metrics.DefaultRegisterer),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			filtered := []ma.Multiaddr{}
			for _, addr := range addrs {
				if manet.IsIPLoopback(addr) {
					continue
				}
				filtered = append(filtered, addr)
			}
			return filtered
		}),
	}
	if cfg.DataDir != "" {
		peerKey, err := loadOrCreatePrivateKey(ctx, cfg.DataDir)
		if err != nil {
			return nil, err
		}
		hostOpts = append(hostOpts, libp2p.Identity(peerKey))
	}
	hostOpts = append(hostOpts, cfg.Libp2pOpts...)
	host, err := libp2p.New(hostOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create host: %w", err)
	}
	protocols := protocolsFromAddrs(host.Addrs())

	dhtOpts := []dht.Option{
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix("/spegel"),
		dht.ProviderManagerOpts(
			records.ProvideValidity(cfg.AdvertiseTTL+(2*cfg.MaxReprovideDelay)),
			records.ProviderAddrTTL(1*time.Hour),
		),
	}
	kdht, err := dht.New(ctx, host, dhtOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create distributed hash table: %w", err)
	}
	connectivityGate := channel.NewGate()
	connectivityGate.Set(true)
	providerOpts := []provider.Option{
		provider.WithConnectivityCallbacks(
			func() { connectivityGate.Set(false) },
			func() { connectivityGate.Set(true) },
			nil,
		),
		provider.WithRouter(kdht),
		provider.WithHost(host),
		provider.WithMessageSender(kdht.MessageSender()),
		provider.WithSelfAddrs(func() []ma.Multiaddr {
			return host.Addrs()
		}),
		provider.WithReprovideInterval(cfg.AdvertiseTTL),
		provider.WithMaxReprovideDelay(cfg.MaxReprovideDelay),
		provider.WithOfflineDelay(0),
		provider.WithConnectivityCheckOnlineInterval(30 * time.Second),
		provider.WithAddLocalRecord(func(h mh.Multihash) error {
			return kdht.ProviderStore().AddProvider(kdht.Context(), h, peer.AddrInfo{ID: host.ID()})
		}),
	}
	prov, err := provider.New(providerOpts...)
	if err != nil {
		return nil, err
	}

	return &P2PRouter{
		bootstrapper:     bs,
		host:             host,
		kdht:             kdht,
		prov:             prov,
		lookupGroup:      &singleflight.Group{},
		lookupCache:      expirable.NewLRU[string, *Iterator](0, nil, lookupCacheTTL),
		connectivityGate: connectivityGate,
		protocols:        protocols,
		registryPort:     uint16(registryPort),
	}, nil
}

func (r *P2PRouter) Host() host.Host {
	return r.host
}

func (r *P2PRouter) Run(ctx context.Context) error {
	log := logr.FromContextOrDiscard(ctx).WithName("p2p")
	log.Info("starting p2p router", "id", r.host.ID())

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		err := r.bootstrapper.Run(gCtx, *host.InfoFromHost(r.host))
		if err != nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return nil
			case <-r.connectivityGate.Wait():
				start := time.Now()
				retryOpts := []resilient.RetryOption{
					resilient.WithOnRetry(func(attempt int, err error) {
						log.Error(err, "failed to run bootstrap", "attempts", attempt+1)
					}),
					resilient.WithLastErrorOnly(),
				}
				err := resilient.Retry(gCtx, 0, resilient.BackoffDelay(50*time.Millisecond, 10*time.Second), func(ctx context.Context) error {
					if !r.connectivityGate.IsOpen() {
						return nil
					}
					err := bootstrapPeers(ctx, r.bootstrapper, r.kdht, r.protocols)
					if err != nil {
						return err
					}
					if r.connectivityGate.IsOpen() {
						return errors.New("bootstrap completed but connectivity has not been reached")
					}
					return nil
				}, retryOpts...)
				if err != nil {
					log.Error(err, "could not run bootstrap")
					continue
				}
				log.Info("bootstrap completed connectivity is reached", "duration", time.Since(start))
			case <-time.After(30 * time.Minute):
				err := bootstrapPeers(gCtx, r.bootstrapper, r.kdht, r.protocols)
				if err != nil {
					log.Error(err, "periodic bootstrap failed")
					continue
				}
			}
		}
	})

	errs := []error{}
	err := g.Wait()
	if err != nil {
		errs = append(errs, err)
	}
	for _, c := range []io.Closer{r.prov, r.kdht, r.host} {
		err := c.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}
	err = errors.Join(errs...)
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) Ready(ctx context.Context) (bool, error) {
	return !r.connectivityGate.IsOpen(), nil
}

func (r *P2PRouter) Lookup(ctx context.Context, key string, count int) (*Iterator, error) {
	log := logr.FromContextOrDiscard(ctx).WithValues("host", r.host.ID().String(), "key", key)
	c, err := createCid(key)
	if err != nil {
		return nil, err
	}

	res, err, _ := r.lookupGroup.Do(c.String(), func() (any, error) {
		iter, ok := r.lookupCache.Get(c.String())
		if ok {
			// Do not refresh if recently run or count is already met.
			if iter.TimeSinceUpdate() < lookupCacheTTL/2 || iter.Count() >= count {
				return iter, nil
			}

			// Open iterator to run refresh.
			iter.Open()
		} else {
			iter = NewIterator()
			r.lookupCache.Add(c.String(), iter)
		}

		addrInfoCh := r.kdht.FindProvidersAsync(ctx, c, count)
		go func() {
			defer iter.Close()

			lookupTimer := prometheus.NewTimer(metrics.ResolveDurHistogram.WithLabelValues("libp2p"))
			for addrInfo := range addrInfoCh {
				lookupTimer.ObserveDuration()

				// Skip self if found in provider store.
				if addrInfo.ID == r.host.ID() {
					continue
				}

				ipAddrs, err := toIPAddrs(addrInfo.Addrs)
				if err != nil {
					log.Error(err, "could not convert address")
					continue
				}
				peer := Peer{
					Host:      addrInfo.ID.String(),
					Addresses: ipAddrs,
					Metadata: PeerMetadata{
						RegistryPort: r.registryPort,
					},
				}
				iter.Add(peer)
			}
		}()
		return iter, nil
	})
	if err != nil {
		return nil, err
	}
	//nolint: errcheck // Impossible to be another type.
	return res.(*Iterator), nil
}

type LookupResult struct {
	Peer     Peer
	Duration time.Duration
}

// Measure returns a list of time results containing the time it took to find each peer.
func (r *P2PRouter) Measure(ctx context.Context, key string) ([]LookupResult, error) {
	c, err := createCid(key)
	if err != nil {
		return nil, err
	}

	addrInfoCh := r.kdht.FindProvidersAsync(ctx, c, 0)

	lookupStart := time.Now()
	results := []LookupResult{}
	for addrInfo := range addrInfoCh {
		d := time.Since(lookupStart)
		ipAddrs, err := toIPAddrs(addrInfo.Addrs)
		if err != nil {
			return nil, err
		}
		res := LookupResult{
			Peer: Peer{
				Host:      addrInfo.ID.String(),
				Addresses: ipAddrs,
				Metadata: PeerMetadata{
					RegistryPort: r.registryPort,
				},
			},
			Duration: d,
		}
		results = append(results, res)
	}
	return results, nil
}

func (r *P2PRouter) Advertise(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	hs := []mh.Multihash{}
	for _, key := range keys {
		c, err := createCid(key)
		if err != nil {
			return err
		}
		h := c.Hash()
		err = r.kdht.ProviderStore().AddProvider(ctx, h, peer.AddrInfo{ID: r.host.ID()})
		if err != nil {
			return err
		}
		hs = append(hs, h)
	}
	err := r.prov.StartProviding(false, hs...)
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) Withdraw(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	mhs := []mh.Multihash{}
	for _, key := range keys {
		c, err := createCid(key)
		if err != nil {
			return err
		}
		mhs = append(mhs, c.Hash())
	}
	err := r.prov.StopProviding(mhs...)
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) ListPeers() ([]Peer, error) {
	peers := []Peer{}
	ids := r.kdht.RoutingTable().ListPeers()
	for _, id := range ids {
		addrs := r.host.Peerstore().Addrs(id)
		ipAddrs, err := toIPAddrs(addrs)
		if err != nil {
			return nil, err
		}
		peer := Peer{
			Host:      id.String(),
			Addresses: ipAddrs,
			Metadata: PeerMetadata{
				RegistryPort: r.registryPort,
			},
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

func (r *P2PRouter) LocalAddresses() ([]netip.Addr, error) {
	ipAddrs, err := toIPAddrs(r.host.Addrs())
	if err != nil {
		return nil, err
	}
	return ipAddrs, nil
}

func toIPAddrs(addrs []ma.Multiaddr) ([]netip.Addr, error) {
	ipAddrs := []netip.Addr{}
	existing := map[string]any{}
	for _, addr := range addrs {
		ip, err := manet.ToIP(addr)
		if err != nil {
			return nil, err
		}
		if _, ok := existing[ip.String()]; ok {
			continue
		}
		ipAddr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nil, errors.New("could not convert to netip address")
		}
		ipAddrs = append(ipAddrs, ipAddr)
	}
	return ipAddrs, nil
}

func listenMultiaddrs(addr string) ([]ma.Multiaddr, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	ipComps := []ma.Component{}
	ip := net.ParseIP(h)
	if ip.To4() != nil {
		ipComp, err := ma.NewComponent("ip4", h)
		if err != nil {
			return nil, err
		}
		ipComps = append(ipComps, *ipComp)
	} else if ip.To16() != nil {
		ipComp, err := ma.NewComponent("ip6", h)
		if err != nil {
			return nil, err
		}
		ipComps = append(ipComps, *ipComp)
	}
	if len(ipComps) == 0 {
		ipComps = []ma.Component{manet.IP6Unspecified[0], manet.IP4Unspecified[0]}
	}

	listenAddrs := []ma.Multiaddr{}
	udpComp, err := ma.NewComponent("udp", p)
	if err != nil {
		return nil, err
	}
	quicComp, err := ma.NewComponent("quic-v1", "")
	if err != nil {
		return nil, err
	}
	tcpComp, err := ma.NewComponent("tcp", p)
	if err != nil {
		return nil, err
	}
	for _, ipComp := range ipComps {
		listenAddrs = append(listenAddrs, ma.Join(ipComp.Multiaddr(), udpComp, quicComp), ma.Join(ipComp.Multiaddr(), tcpComp))
	}
	return listenAddrs, nil
}

func protocolsFromAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	protocolSet := map[string]ma.Multiaddr{}
	for _, addr := range addrs {
		_, protocol := ma.SplitFirst(addr)
		protocolSet[protocol.String()] = protocol
	}
	protocols := []ma.Multiaddr{}
	for _, v := range protocolSet {
		protocols = append(protocols, v)
	}
	return protocols
}

func createCid(key string) (cid.Cid, error) {
	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(mc.Raw),
		MhType:   mh.SHA2_256,
		MhLength: -1,
	}
	c, err := pref.Sum([]byte(key))
	if err != nil {
		return cid.Cid{}, err
	}
	return c, nil
}

func addrsEqual(a1, a2 []ma.Multiaddr) bool {
	for _, a1Addr := range a1 {
		for _, a2Addr := range a2 {
			if a1Addr.Equal(a2Addr) {
				return true
			}
		}
	}
	return false
}

func bootstrapPeers(ctx context.Context, bs Bootstrapper, kdht *dht.IpfsDHT, protocols []ma.Multiaddr) error {
	// Attempt to connect to bootstrap peers.
	bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, 30*time.Second)
	defer bootstrapCancel()

	addrInfos, err := bs.Get(bootstrapCtx)
	if err != nil {
		return err
	}
	errs := []error{}
	self := *host.InfoFromHost(kdht.Host())
	for _, addrInfo := range addrInfos {
		// If ID is not empty and match it is self.
		if self.ID != "" && addrInfo.ID != "" && self.ID == addrInfo.ID {
			continue
		}

		// Add protocol from host listener if missing.
		modifiedAddrs := []ma.Multiaddr{}
		for _, addr := range addrInfo.Addrs {
			_, remainder := ma.SplitFirst(addr)
			if len(remainder) > 0 {
				modifiedAddrs = append(modifiedAddrs, addr)
				continue
			}
			for _, protocol := range protocols {
				modifiedAddrs = append(modifiedAddrs, ma.Join(addr, protocol))
			}
		}
		addrInfo.Addrs = modifiedAddrs

		matches := addrsEqual(self.Addrs, addrInfo.Addrs)
		if matches {
			continue
		}

		if addrInfo.ID == "" {
			priv, _, err := crypto.GenerateEd25519Key(nil)
			if err != nil {
				return err
			}
			id, err := peer.IDFromPrivateKey(priv)
			if err != nil {
				return err
			}
			addrInfo.ID = id
			err = kdht.Host().Connect(bootstrapCtx, addrInfo)
			var mismatchErr sec.ErrPeerIDMismatch
			if !errors.As(err, &mismatchErr) {
				errs = append(errs, err)
				continue
			}
			kdht.Host().Peerstore().ClearAddrs(addrInfo.ID)
			kdht.Host().Peerstore().RemovePeer(addrInfo.ID)
			addrInfo.ID = mismatchErr.Actual
		}

		err := kdht.Host().Connect(bootstrapCtx, addrInfo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}
	if len(errs) == len(addrInfos) {
		return errors.Join(errs...)
	}

	// Refresh routing table.
	if kdht.RoutingTable().Size() == 0 {
		return errors.New("routing table is empty after bootstrapping")
	}
	errCh := kdht.RefreshRoutingTable()
	err = <-errCh
	if err != nil {
		return err
	}
	return nil
}

func loadOrCreatePrivateKey(ctx context.Context, dataDir string) (crypto.PrivKey, error) {
	keyPath := filepath.Join(dataDir, "private.key")
	log := logr.FromContextOrDiscard(ctx).WithValues("path", keyPath)
	err := os.MkdirAll(dataDir, 0o755)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(keyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, os.ErrNotExist) {
		log.Info("creating a new private key")
		privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, err
		}
		rawBytes, err := privKey.Raw()
		if err != nil {
			return nil, err
		}
		pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(ed25519.PrivateKey(rawBytes))
		if err != nil {
			return nil, err
		}
		block := &pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: pkcs8Bytes,
		}
		pemData := pem.EncodeToMemory(block)
		err = os.WriteFile(keyPath, pemData, 0o600)
		if err != nil {
			return nil, err
		}
		return privKey, nil
	}
	log.Info("loading the private key from data directory")
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("invalid PEM block type %s", block.Type)
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	edKey, ok := parsedKey.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	privKey, err := crypto.UnmarshalEd25519PrivateKey(edKey)
	if err != nil {
		return nil, err
	}
	return privKey, nil
}
