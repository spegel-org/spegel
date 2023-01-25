package discover

import (
	"context"
	"net"
)

type Discover interface {
	GetPeers(ctx context.Context) ([]string, error)
}

type DNS struct {
	serviceName string
}

func NewDNS(serviceName string) Discover {
	return DNS{serviceName: serviceName}
}

func (d DNS) GetPeers(ctx context.Context) ([]string, error) {
	// TODO: Evaluate github.com/miekg/dns
	// TODO: Explore caching results for better performance, might be useless considering TTL
	addrs, err := net.LookupHost(d.serviceName)
	if err != nil {
		return nil, err
	}
	peers := []string{}
	for _, addr := range addrs {
		peers = append(peers, addr)
	}
	return peers, nil
}

type Mock struct {
	peers []string
}

func NewMock(peers []string) Discover {
	return Mock{peers: peers}
}

func (m Mock) GetPeers(ctx context.Context) ([]string, error) {
	return m.peers, nil
}
