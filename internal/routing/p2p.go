package routing

import (
	"context"
	"fmt"
	"net"
	"strings"

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
)

type P2PRouter struct {
	b            Bootstrapper
	host         host.Host
	kdht         *dht.IpfsDHT
	rd           *routing.RoutingDiscovery
	registryPort string
}

func NewP2PRouter(ctx context.Context, addr string, b Bootstrapper, registryPort string) (Router, error) {
	log := logr.FromContextOrDiscard(ctx).WithName("p2p")

	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if h == "" {
		h = "0.0.0.0"
	}
	multiAddr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", h, p))
	if err != nil {
		return nil, fmt.Errorf("could not create host multi address: %w", err)
	}
	addrFactoryOpt := libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return ma.FilterAddrs(addrs, func(a ma.Multiaddr) bool { return !manet.IsIPLoopback(a) })
	})
	host, err := libp2p.New(libp2p.ListenAddrs(multiAddr), addrFactoryOpt)
	if err != nil {
		return nil, fmt.Errorf("could not create host: %w", err)
	}
	if len(host.Addrs()) > 1 {
		addrs := []string{}
		for _, addr := range host.Addrs() {
			addrs = append(addrs, addr.String())
		}
		return nil, fmt.Errorf("expected singled host address got %s", strings.Join(addrs, ", "))
	}
	self := fmt.Sprintf("%s/p2p/%s", host.Addrs()[0].String(), host.ID().Pretty())
	log.Info("starting p2p router", "id", self)

	err = b.Run(ctx, self)
	if err != nil {
		return nil, err
	}

	bootstrapPeerOpt := dht.BootstrapPeersFunc(func() []peer.AddrInfo {
		addrInfo, err := b.GetAddress()
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
	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("could not boostrap distributed hash table: %w", err)
	}
	rd := routing.NewRoutingDiscovery(kdht)

	return &P2PRouter{
		b:            b,
		host:         host,
		kdht:         kdht,
		rd:           rd,
		registryPort: registryPort,
	}, nil
}

func (r *P2PRouter) Close() error {
	return r.host.Close()
}

func (r *P2PRouter) HasMirrors() (bool, error) {
	addrInfo, err := r.b.GetAddress()
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

func (r *P2PRouter) Resolve(ctx context.Context, key string, allowSelf bool, count int) (<-chan string, error) {
	log := logr.FromContextOrDiscard(ctx).WithValues("host", r.host.ID().Pretty(), "key", key)
	c, err := createCid(key)
	if err != nil {
		return nil, err
	}
	addrCh := r.rd.FindProvidersAsync(ctx, c, count)
	peerCh := make(chan string, count)
	go func() {
		for info := range addrCh {
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
			v, err := info.Addrs[0].ValueForProtocol(ma.P_IP4)
			if err != nil {
				log.Error(err, "could not get IPV4 address")
				continue
			}
			// Combine peer with registry port to create mirror endpoint.
			peerCh <- fmt.Sprintf("http://%s:%s", v, r.registryPort)
		}
		close(peerCh)
	}()
	return peerCh, nil
}

func (r *P2PRouter) Advertise(ctx context.Context, keys []string) error {
	logr.FromContextOrDiscard(ctx).V(10).Info("advertising keys", "host", r.host.ID().Pretty(), "keys", keys)
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
