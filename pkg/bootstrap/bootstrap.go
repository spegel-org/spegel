package bootstrap

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Bootstrapper interface {
	Run(ctx context.Context, id string) error
	GetAddress() (*peer.AddrInfo, error)
}
