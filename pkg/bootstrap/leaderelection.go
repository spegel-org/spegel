package bootstrap

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type LeaderElectionBootstrapper struct {
	leaderElectionNamespace string
	leaderElectioName       string
	cs                      kubernetes.Interface
	initCh                  chan interface{}
	mx                      sync.RWMutex
	id                      string
}

func NewLeaderElectionBootstrapper(cs kubernetes.Interface, namespace, name string) Bootstrapper {
	k := &LeaderElectionBootstrapper{
		leaderElectionNamespace: namespace,
		leaderElectioName:       name,
		cs:                      cs,
		initCh:                  make(chan interface{}),
	}
	return k
}

func (k *LeaderElectionBootstrapper) Run(ctx context.Context, id string) error {
	lockCfg := resourcelock.ResourceLockConfig{
		Identity: id,
	}
	rl, err := resourcelock.New(resourcelock.ConfigMapsLeasesResourceLock, k.leaderElectionNamespace, k.leaderElectioName, k.cs.CoreV1(), k.cs.CoordinationV1(), lockCfg)
	if err != nil {
		return err
	}
	leCfg := leaderelection.LeaderElectionConfig{
		Lock:            rl,
		ReleaseOnCancel: true,
		LeaseDuration:   10 * time.Second,
		RenewDeadline:   5 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {},
			OnStoppedLeading: func() {},
			OnNewLeader: func(identity string) {
				if identity == resourcelock.UnknownLeader {
					return
				}
				// Close channel if not already closed
				select {
				case <-k.initCh:
					break
				default:
					close(k.initCh)
				}

				k.mx.Lock()
				defer k.mx.Unlock()
				k.id = identity
			},
		},
	}
	go leaderelection.RunOrDie(ctx, leCfg)
	return nil
}

func (k *LeaderElectionBootstrapper) GetAddress() (*peer.AddrInfo, error) {
	<-k.initCh
	k.mx.RLock()
	defer k.mx.RUnlock()

	addr, err := multiaddr.NewMultiaddr(k.id)
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}
	return addrInfo, err
}
