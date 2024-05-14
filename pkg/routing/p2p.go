package routing

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spegel-org/spegel/pkg/metrics"
)

const KeyTTL = 10 * time.Minute

type P2PRouter struct {
	bootstrapper Bootstrapper
	host         host.Host
	kdht         *dht.IpfsDHT
	rd           *routing.RoutingDiscovery
	registryPort uint16
}

func NewP2PRouter(ctx context.Context, addr string, bootstrapper Bootstrapper, registryPortStr string, opts ...libp2p.Option) (*P2PRouter, error) {
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
	opts = append(opts,
		libp2p.ListenAddrs(multiAddrs...),
		libp2p.PrometheusRegisterer(metrics.DefaultRegisterer),
		addrFactoryOpt,
	)
	host, err := libp2p.New(opts...)
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

	log := logr.FromContextOrDiscard(ctx).WithName("p2p")
	bootstrapPeerOpt := dht.BootstrapPeersFunc(func() []peer.AddrInfo {
		addrInfo, err := bootstrapper.Get()
		if err != nil {
			log.Error(err, "could not get bootstrap addresses")
			return nil
		}
		if addrInfo.ID == host.ID() {
			log.Info("leader is self skipping connection to bootstrap node")
			return nil
		}
		return []peer.AddrInfo{*addrInfo}
	})
	dhtOpts := []dht.Option{
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix("/spegel"),
		dht.DisableValues(),
		dht.MaxRecordAge(KeyTTL),
		bootstrapPeerOpt,
	}
	kdht, err := dht.New(ctx, host, dhtOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not create distributed hash table: %w", err)
	}
	rd := routing.NewRoutingDiscovery(kdht)

	return &P2PRouter{
		bootstrapper: bootstrapper,
		host:         host,
		kdht:         kdht,
		rd:           rd,
		registryPort: uint16(registryPort),
	}, nil
}

func (r *P2PRouter) Run(ctx context.Context) error {
	self := fmt.Sprintf("%s/p2p/%s", r.host.Addrs()[0].String(), r.host.ID().String())
	logr.FromContextOrDiscard(ctx).WithName("p2p").Info("starting p2p router", "id", self)
	if err := r.kdht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("could not boostrap distributed hash table: %w", err)
	}
	err := r.bootstrapper.Run(ctx, self)
	if err != nil {
		return err
	}
	return nil
}

func (r *P2PRouter) Close() error {
	return r.host.Close()
}

func (r *P2PRouter) Ready() (bool, error) {
	addrInfo, err := r.bootstrapper.Get()
	if err != nil {
		return false, err
	}
	if addrInfo.ID == r.host.ID() {
		return true, nil
	}
	if r.kdht.RoutingTable().Size() == 0 {
		return false, nil
	}
	return true, nil
}

func (r *P2PRouter) Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan netip.AddrPort, error) {
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
	addrCh := r.rd.FindProvidersAsync(ctx, c, count)
	peerCh := make(chan netip.AddrPort, peerBufferSize)
	go func() {
		resolveTimer := prometheus.NewTimer(metrics.ResolveDurHistogram.WithLabelValues("libp2p"))
		for info := range addrCh {
			resolveTimer.ObserveDuration()
			if !allowSelf && info.ID == r.host.ID() {
				continue
			}
			if len(info.Addrs) != 1 {
				addrs := []string{}
				for _, addr := range info.Addrs {
					addrs = append(addrs, addr.String())
				}
				log.Info("expected address list to only contain a single item", "addresses", strings.Join(addrs, ", "))
				continue
			}
			ipAddr, err := ipInMultiaddr(info.Addrs[0])
			if err != nil {
				log.Error(err, "could not get IP address")
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

func (r *P2PRouter) Advertise(ctx context.Context, keys []string) error {
	logr.FromContextOrDiscard(ctx).V(4).Info("advertising keys", "host", r.host.ID().String(), "keys", keys)
	for _, key := range keys {
		c, err := createCid(key)
		if err != nil {
			return err
		}
		err = r.rd.Provide(ctx, c, false)
		if err != nil {
			return err
		}
	}
	return nil
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

func ipInMultiaddr(multiAddr ma.Multiaddr) (netip.Addr, error) {
	for _, p := range []int{ma.P_IP6, ma.P_IP4} {
		v, err := multiAddr.ValueForProtocol(p)
		if errors.Is(err, ma.ErrProtocolNotFound) {
			continue
		}
		if err != nil {
			return netip.Addr{}, err
		}
		ipAddr, err := netip.ParseAddr(v)
		if err != nil {
			return netip.Addr{}, err
		}
		return ipAddr, nil
	}
	return netip.Addr{}, errors.New("IP not found in address")
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
