package routing

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-logr/logr"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/multiformats/go-multiaddr"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
)

const KeyTTL = 10 * time.Minute

type Router interface {
	Close() error
	Resolve(ctx context.Context, key string) (string, bool, error)
	Advertise(ctx context.Context, keys []string) error
}

type P2PRouter struct {
	host host.Host
	rd   *routing.RoutingDiscovery
}

func NewP2PRouter(ctx context.Context, addr string, b Bootstrapper) (Router, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if h == "" {
		h = "0.0.0.0"
	}
	multiAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", h, p))
	if err != nil {
		return nil, err
	}
	host, err := libp2p.New(libp2p.ListenAddrs(multiAddr))
	if err != nil {
		return nil, err
	}
	// TODO: Implement bootstrap peers func in case of total failure.
	kdht, err := dht.New(ctx, host, dht.Mode(dht.ModeServer), dht.ProtocolPrefix("/spegel"), dht.DisableValues(), dht.MaxRecordAge(KeyTTL))
	if err != nil {
		return nil, err
	}
	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, err
	}
	rd := routing.NewRoutingDiscovery(kdht)

	// Get bootstrap addr
	self := fmt.Sprintf("%s/p2p/%s", host.Addrs()[0].String(), host.ID().Pretty())
	bootstrapAddr, err := b.GetAddress(ctx, self)
	if err != nil {
		return nil, err
	}

	// Connect to bootstrap node if it is not us.
	if bootstrapAddr != self {
		log := logr.FromContextOrDiscard(ctx)
		log.Info("connecting to bootstrap node", "addr", bootstrapAddr)
		bootstrapMultiAddr, err := multiaddr.NewMultiaddr(bootstrapAddr)
		if err != nil {
			return nil, err
		}
		info, err := peer.AddrInfoFromP2pAddr(bootstrapMultiAddr)
		if err != nil {
			return nil, err
		}
		err = host.Connect(ctx, *info)
		if err != nil {
			return nil, err
		}
		log.Info("established connection with bootstrap node", "info", info)
	}

	return &P2PRouter{
		host: host,
		rd:   rd,
	}, nil
}

func (r *P2PRouter) Close() error {
	return r.host.Close()
}

func (r *P2PRouter) Resolve(ctx context.Context, key string) (string, bool, error) {
	c, err := createCid(key)
	if err != nil {
		return "", false, err
	}
	ch := r.rd.FindProvidersAsync(ctx, c, 1)
	for {
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case info := <-ch:
			if len(info.Addrs) == 0 {
				return "", false, fmt.Errorf("invalid node found with empty address list")
			}
			addr := info.Addrs[0]
			v, err := addr.ValueForProtocol(multiaddr.P_IP4)
			if err != nil {
				return "", false, err
			}
			if v == "" {
				return "", false, fmt.Errorf("unexpected empty ip address")
			}
			return v, true, nil
		}
	}
}

func (r *P2PRouter) Advertise(ctx context.Context, keys []string) error {
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