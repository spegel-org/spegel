package routing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spegel-org/spegel/pkg/metrics"
)

const KeyTTL = 10 * time.Minute

type P2PRouterConfig struct {
	DataDir    string
	Libp2pOpts []libp2p.Option
}

func (cfg *P2PRouterConfig) Apply(opts ...P2PRouterOption) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}

type P2PRouterOption func(cfg *P2PRouterConfig) error

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

var _ Router = &P2PRouter{}

type P2PRouter struct {
	bootstrapper Bootstrapper
	host         host.Host
	kdht         *dht.IpfsDHT
	rd           *routing.RoutingDiscovery
	registryPort uint16
}

func NewP2PRouter(ctx context.Context, addr string, bs Bootstrapper, registryPortStr string, opts ...P2PRouterOption) (*P2PRouter, error) {
	cfg := P2PRouterConfig{}
	err := cfg.Apply(opts...)
	if err != nil {
		return nil, err
	}

	registryPort, err := strconv.ParseUint(registryPortStr, 10, 16)
	if err != nil {
		return nil, err
	}

	multiAddrs, err := listenMultiaddrs(addr)
	if err != nil {
		return nil, err
	}
	addrFactoryOpt := libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
		var ip4Ma, ip6Ma ma.Multiaddr
		for _, addr := range addrs {
			if manet.IsIPLoopback(addr) {
				continue
			}
			if isIp6(addr) {
				ip6Ma = addr
				continue
			}
			ip4Ma = addr
		}
		if ip6Ma != nil {
			return []ma.Multiaddr{ip6Ma}
		}
		if ip4Ma != nil {
			return []ma.Multiaddr{ip4Ma}
		}
		return nil
	})
	libp2pOpts := []libp2p.Option{
		libp2p.ListenAddrs(multiAddrs...),
		libp2p.PrometheusRegisterer(metrics.DefaultRegisterer),
		addrFactoryOpt,
	}
	if cfg.DataDir != "" {
		peerKey, err := loadOrCreatePrivateKey(ctx, cfg.DataDir)
		if err != nil {
			return nil, err
		}
		libp2pOpts = append(libp2pOpts, libp2p.Identity(peerKey))
	}
	libp2pOpts = append(libp2pOpts, cfg.Libp2pOpts...)
	host, err := libp2p.New(libp2pOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create host: %w", err)
	}
	if len(host.Addrs()) != 1 {
		addrs := []string{}
		for _, addr := range host.Addrs() {
			addrs = append(addrs, addr.String())
		}
		return nil, fmt.Errorf("expected single host address but got %d %s", len(addrs), strings.Join(addrs, ", "))
	}

	dhtOpts := []dht.Option{
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix("/spegel"),
		dht.DisableValues(),
		dht.MaxRecordAge(KeyTTL),
		dht.BootstrapPeersFunc(bootstrapFunc(ctx, bs, host)),
	}
	kdht, err := dht.New(ctx, host, dhtOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create distributed hash table: %w", err)
	}
	rd := routing.NewRoutingDiscovery(kdht)

	return &P2PRouter{
		bootstrapper: bs,
		host:         host,
		kdht:         kdht,
		rd:           rd,
		registryPort: uint16(registryPort),
	}, nil
}

func (r *P2PRouter) Run(ctx context.Context) (err error) {
	logr.FromContextOrDiscard(ctx).WithName("p2p").Info("starting p2p router", "id", r.host.ID())
	if err := r.kdht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("could not bootstrap distributed hash table: %w", err)
	}
	defer func() {
		cerr := r.host.Close()
		if cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	err = r.bootstrapper.Run(ctx, *host.InfoFromHost(r.host))
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) Ready(ctx context.Context) (bool, error) {
	addrInfos, err := r.bootstrapper.Get(ctx)
	if err != nil {
		return false, err
	}
	if len(addrInfos) == 0 {
		return false, nil
	}
	if len(addrInfos) == 1 {
		matches := addrInfoMatches(*host.InfoFromHost(r.host), addrInfos[0])
		if matches {
			return true, nil
		}
	}
	if r.kdht.RoutingTable().Size() > 0 {
		return true, nil
	}
	err = r.kdht.Bootstrap(ctx)
	if err != nil {
		return false, err
	}
	return false, nil
}

func (r *P2PRouter) Resolve(ctx context.Context, key string, count int) (<-chan netip.AddrPort, error) {
	log := logr.FromContextOrDiscard(ctx).WithValues("host", r.host.ID().String(), "key", key)
	c, err := createCid(key)
	if err != nil {
		return nil, err
	}
	// If using unlimited retries (count=0), ensure that the peer address channel
	// does not become blocking by using a reasonable non-zero buffer size.
	peerBufferSize := count
	if peerBufferSize == 0 {
		peerBufferSize = 20
	}
	addrInfoCh := r.rd.FindProvidersAsync(ctx, c, count)
	peerCh := make(chan netip.AddrPort, peerBufferSize)
	go func() {
		resolveTimer := prometheus.NewTimer(metrics.ResolveDurHistogram.WithLabelValues("libp2p"))
		for addrInfo := range addrInfoCh {
			resolveTimer.ObserveDuration()
			if len(addrInfo.Addrs) != 1 {
				addrs := []string{}
				for _, addr := range addrInfo.Addrs {
					addrs = append(addrs, addr.String())
				}
				log.Info("expected address list to only contain a single item", "addresses", strings.Join(addrs, ", "))
				continue
			}
			ip, err := manet.ToIP(addrInfo.Addrs[0])
			if err != nil {
				log.Error(err, "could not get IP address")
				continue
			}
			ipAddr, ok := netip.AddrFromSlice(ip)
			if !ok {
				log.Error(errors.New("IP is not IPV4 or IPV6"), "could not convert IP")
				continue
			}
			peer := netip.AddrPortFrom(ipAddr, r.registryPort)
			// Don't block if the client has disconnected before reading all values from the channel
			select {
			case peerCh <- peer:
			default:
				log.V(4).Info("mirror endpoint dropped: peer channel is full")
			}
		}
		close(peerCh)
	}()
	return peerCh, nil
}

func (r *P2PRouter) Advertise(ctx context.Context, keys []string, brdcst bool) error {
	logr.FromContextOrDiscard(ctx).V(4).Info("advertising keys", "host", r.host.ID().String(), "keys", keys)
	for _, key := range keys {
		c, err := createCid(key)
		if err != nil {
			return err
		}
		err = r.rd.Provide(ctx, c, brdcst)
		if err != nil {
			return err
		}
	}
	return nil
}

func bootstrapFunc(ctx context.Context, bootstrapper Bootstrapper, h host.Host) func() []peer.AddrInfo {
	log := logr.FromContextOrDiscard(ctx).WithName("p2p")
	return func() []peer.AddrInfo {
		bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer bootstrapCancel()

		// TODO (phillebaba): Consider if we should do a best effort bootstrap without host address.
		hostAddrs := h.Addrs()
		if len(hostAddrs) == 0 {
			return nil
		}
		var hostPort ma.Component
		ma.ForEach(hostAddrs[0], func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_TCP {
				hostPort = c
				return false
			}
			return true
		})

		addrInfos, err := bootstrapper.Get(bootstrapCtx)
		if err != nil {
			log.Error(err, "could not get bootstrap addresses")
			return nil
		}
		filteredAddrInfos := []peer.AddrInfo{}
		for _, addrInfo := range addrInfos {
			// Skip addresses that match host.
			matches := addrInfoMatches(*host.InfoFromHost(h), addrInfo)
			if matches {
				log.Info("skipping bootstrap peer that is same as host")
				continue
			}

			// Add port to address if it is missing.
			modifiedAddrs := []ma.Multiaddr{}
			for _, addr := range addrInfo.Addrs {
				hasPort := false
				ma.ForEach(addr, func(c ma.Component) bool {
					if c.Protocol().Code == ma.P_TCP {
						hasPort = true
						return false
					}
					return true
				})
				if hasPort {
					modifiedAddrs = append(modifiedAddrs, addr)
					continue
				}
				modifiedAddrs = append(modifiedAddrs, ma.Join(addr, &hostPort))
			}
			addrInfo.Addrs = modifiedAddrs

			// Resolve ID if it is missing.
			if addrInfo.ID != "" {
				filteredAddrInfos = append(filteredAddrInfos, addrInfo)
				continue
			}
			addrInfo.ID = "id"
			err = h.Connect(bootstrapCtx, addrInfo)
			var mismatchErr sec.ErrPeerIDMismatch
			if !errors.As(err, &mismatchErr) {
				log.Error(err, "could not get peer id")
				continue
			}
			addrInfo.ID = mismatchErr.Actual
			filteredAddrInfos = append(filteredAddrInfos, addrInfo)
		}
		if len(filteredAddrInfos) == 0 {
			log.Info("no bootstrap nodes found")
			return nil
		}
		return filteredAddrInfos
	}
}

func listenMultiaddrs(addr string) ([]ma.Multiaddr, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	tcpComp, err := ma.NewMultiaddr(fmt.Sprintf("/tcp/%s", p))
	if err != nil {
		return nil, err
	}
	ipComps := []ma.Multiaddr{}
	ip := net.ParseIP(h)
	if ip.To4() != nil {
		ipComp, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s", h))
		if err != nil {
			return nil, fmt.Errorf("could not create host multi address: %w", err)
		}
		ipComps = append(ipComps, ipComp)
	} else if ip.To16() != nil {
		ipComp, err := ma.NewMultiaddr(fmt.Sprintf("/ip6/%s", h))
		if err != nil {
			return nil, fmt.Errorf("could not create host multi address: %w", err)
		}
		ipComps = append(ipComps, ipComp)
	}
	if len(ipComps) == 0 {
		ipComps = []ma.Multiaddr{manet.IP6Unspecified, manet.IP4Unspecified}
	}
	multiAddrs := []ma.Multiaddr{}
	for _, ipComp := range ipComps {
		multiAddrs = append(multiAddrs, ipComp.Encapsulate(tcpComp))
	}
	return multiAddrs, nil
}

func isIp6(m ma.Multiaddr) bool {
	c, _ := ma.SplitFirst(m)
	if c == nil || c.Protocol().Code != ma.P_IP6 {
		return false
	}
	return true
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

func addrInfoMatches(a, b peer.AddrInfo) bool {
	// Skip self when address ID matches host ID.
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}

	// Skip self when IP matches
	for _, aAddr := range a.Addrs {
		if aAddr[0].Code() != ma.P_IP4 && aAddr[0].Code() != ma.P_IP6 {
			continue
		}
		for _, bAddr := range b.Addrs {
			if aAddr[0].Code() != bAddr[0].Code() {
				continue
			}
			if aAddr[0].Value() != bAddr[0].Value() {
				continue
			}
			return true
		}
	}
	return false
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
