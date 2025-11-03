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
	"sync/atomic"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-logr/logr"
	"github.com/hashicorp/golang-lru/v2/expirable"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/metrics"
)

const KeyTTL = 10 * time.Minute

const bootstrapSizeThreshold = 10

type P2PRouterConfig struct {
	DataDir    string
	Libp2pOpts []libp2p.Option
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

var _ Router = &P2PRouter{}

type P2PRouter struct {
	bootstrapper  Bootstrapper
	host          host.Host
	kdht          *dht.IpfsDHT
	balancerGroup *singleflight.Group
	balancerCache *expirable.LRU[string, *ClosableBalancer]
	registryPort  uint16
	isOnline      atomic.Bool
}

func NewP2PRouter(ctx context.Context, addr string, bs Bootstrapper, registryPortStr string, opts ...P2PRouterOption) (*P2PRouter, error) {
	cfg := P2PRouterConfig{}
	err := option.Apply(&cfg, opts...)
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
	hostOpts := []libp2p.Option{
		libp2p.ListenAddrs(multiAddrs...),
		libp2p.PrometheusRegisterer(metrics.DefaultRegisterer),
		addrFactoryOpt,
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
		dht.MaxRecordAge(KeyTTL),
	}
	kdht, err := dht.New(ctx, host, dhtOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create distributed hash table: %w", err)
	}

	return &P2PRouter{
		bootstrapper:  bs,
		host:          host,
		kdht:          kdht,
		registryPort:  uint16(registryPort),
		balancerGroup: &singleflight.Group{},
		balancerCache: expirable.NewLRU[string, *ClosableBalancer](0, nil, 5*time.Second),
		isOnline:      atomic.Bool{},
	}, nil
}

func (r *P2PRouter) Run(ctx context.Context) error {
	logr.FromContextOrDiscard(ctx).WithName("p2p").Info("starting p2p router", "id", r.host.ID())

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		err := r.bootstrapper.Run(gCtx, *host.InfoFromHost(r.host))
		if err != nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		var lastBootstrap time.Time
		onlineCh := time.After(0)
		for {
			select {
			case <-gCtx.Done():
				return nil
			case <-onlineCh:
				var err error
				lastBootstrap, err = ensureOnline(gCtx, r.bootstrapper, r.kdht, &r.isOnline, lastBootstrap)
				if err != nil {
					return err
				}
				onlineCh = time.After(30 * time.Second)
			}
		}
	})

	err := g.Wait()
	cerr := r.kdht.Close()
	if cerr != nil {
		err = errors.Join(err, cerr)
	}
	cerr = r.host.Close()
	if cerr != nil {
		err = errors.Join(err, cerr)
	}
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) Ready(ctx context.Context) (bool, error) {
	return r.isOnline.Load(), nil
}

func (r *P2PRouter) Lookup(ctx context.Context, key string, count int) (Balancer, error) {
	log := logr.FromContextOrDiscard(ctx).WithValues("host", r.host.ID().String(), "key", key)
	c, err := createCid(key)
	if err != nil {
		return nil, err
	}

	bal, err, _ := r.balancerGroup.Do(c.String(), func() (any, error) {
		cb, ok := r.balancerCache.Get(c.String())
		if !ok {
			cb = NewClosableBalancer(NewRoundRobin())
			r.balancerCache.Add(c.String(), cb)
		}

		if ok {
			// If not closed it means query is still running.
			if cb.closeCtx.Err() == nil {
				return cb, nil
			}
			// Don't refresh if min count is already met.
			if count > 0 && cb.Size() >= count {
				cb.Close()
				return cb, nil
			}

			// If we are running a refresh query we ant a new closer.
			cb = NewClosableBalancer(cb.Balancer)
			r.balancerCache.Add(c.String(), cb)
		}

		addrInfoCh := r.kdht.FindProvidersAsync(ctx, c, count)
		go func() {
			defer cb.Close()

			lookupTimer := prometheus.NewTimer(metrics.ResolveDurHistogram.WithLabelValues("libp2p"))
			for addrInfo := range addrInfoCh {
				lookupTimer.ObserveDuration()
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
				cb.Add(peer)
			}
		}()
		return cb, nil
	})
	if err != nil {
		return nil, err
	}
	//nolint: errcheck // Impossible to be another type other than Balancer.
	return bal.(Balancer), nil
}

func (r *P2PRouter) Advertise(ctx context.Context, keys []string) error {
	logr.FromContextOrDiscard(ctx).V(4).Info("advertising keys", "host", r.host.ID().String(), "keys", keys)
	for _, key := range keys {
		c, err := createCid(key)
		if err != nil {
			return err
		}
		err = r.kdht.Provide(ctx, c, true)
		if err != nil {
			return err
		}
	}
	return nil
}

type Peer struct {
	Address string
	ID      string
}

func (r *P2PRouter) ListPeers() ([]Peer, error) {
	peers := []Peer{}
	ids := r.kdht.RoutingTable().ListPeers()
	for _, id := range ids {
		addrs := r.host.Peerstore().Addrs(id)
		if len(addrs) == 0 {
			continue
		}
		if len(addrs) > 1 {
			return nil, errors.New("dual stack not supported")
		}
		netAddr, err := manet.ToNetAddr(addrs[0])
		if err != nil {
			return nil, err
		}
		peers = append(peers, Peer{Address: netAddr.String(), ID: id.String()})
	}
	return peers, nil
}

func (r *P2PRouter) LocalAddress() string {
	addrs := r.host.Addrs()
	var ip4Addr, ip6Addr netip.Addr

	for _, addr := range addrs {
		if manet.IsIPLoopback(addr) {
			continue
		}

		ip, err := manet.ToIP(addr)
		if err != nil {
			continue
		}

		ipAddr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}

		if ipAddr.Is6() {
			ip6Addr = ipAddr
		} else if ipAddr.Is4() {
			ip4Addr = ipAddr
		}
	}

	if ip6Addr.IsValid() {
		return ip6Addr.String()
	}
	if ip4Addr.IsValid() {
		return ip4Addr.String()
	}

	return ""
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

func ensureOnline(ctx context.Context, bs Bootstrapper, kdht *dht.IpfsDHT, isOnline *atomic.Bool, lastBootstrap time.Time) (time.Time, error) {
	shouldBootstrap := func() bool {
		if kdht.RoutingTable().Size() == 0 {
			return true
		}
		if time.Since(lastBootstrap) > 30*time.Minute {
			return true
		}
		if time.Since(lastBootstrap) > 2*time.Minute && kdht.RoutingTable().Size() < bootstrapSizeThreshold {
			return true
		}
		return false
	}()
	if !shouldBootstrap {
		return lastBootstrap, nil
	}

	logr.FromContextOrDiscard(ctx).Info("running bootstrap")

	retryOpts := []retry.Option{
		retry.Context(ctx),
		retry.Attempts(0),
		retry.DelayType(retry.FullJitterBackoffDelay),
		retry.Delay(100 * time.Millisecond),
		retry.MaxDelay(5 * time.Second),
		retry.OnRetry(func(attempt uint, err error) {
			logr.FromContextOrDiscard(ctx).Error(err, "failed to run bootstrap", "attempts", attempt+1)
		}),
	}
	err := retry.Do(func() error {
		bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, 5*time.Second)
		defer bootstrapCancel()
		addrInfos, err := bs.Get(bootstrapCtx)
		if err == nil && len(addrInfos) == 1 {
			matches := addrInfoMatches(*host.InfoFromHost(kdht.Host()), addrInfos[0])
			if matches {
				logr.FromContextOrDiscard(ctx).Info("assuming online as only bootstrap peer found is self")
				return nil
			}
		}
		if kdht.RoutingTable().Size() == 0 {
			isOnline.Store(false)
		}
		if err != nil {
			return err
		}
		if len(addrInfos) == 0 {
			// Succeed if we cant get bootstrap peers but others have connected to us.
			if kdht.RoutingTable().Size() > 0 {
				return nil
			}
			return errors.New("no bootstrap peers found")
		}

		// Get port from host address.
		hostAddrs := kdht.Host().Addrs()
		if len(hostAddrs) == 0 {
			return errors.New("host does not have any addresses")
		}
		var hostPort ma.Component
		ma.ForEach(hostAddrs[0], func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_TCP {
				hostPort = c
				return false
			}
			return true
		})

		// Attempt to connect to bootstrap peers.
		errs := []error{}
		self := *host.InfoFromHost(kdht.Host())
		for _, addrInfo := range addrInfos {
			matches := addrInfoMatches(self, addrInfo)
			if matches {
				continue
			}

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

			if addrInfo.ID == "" {
				addrInfo.ID = "id"
				err := kdht.Host().Connect(ctx, addrInfo)
				var mismatchErr sec.ErrPeerIDMismatch
				if !errors.As(err, &mismatchErr) {
					errs = append(errs, err)
					continue
				}
				addrInfo.ID = mismatchErr.Actual
			}

			err := kdht.Host().Connect(ctx, addrInfo)
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
	}, retryOpts...)
	if err != nil {
		return lastBootstrap, err
	}

	logr.FromContextOrDiscard(ctx).Info("completed bootstrap", "peers", kdht.RoutingTable().Size())
	isOnline.Store(true)
	return time.Now(), nil
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
