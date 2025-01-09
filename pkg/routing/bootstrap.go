package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Bootstrapper resolves peers to bootstrap with.
type Bootstrapper interface {
	// Run starts the bootstrap process. Should be blocking even if not needed.
	Run(ctx context.Context, id string) error
	// Get returns a list of peers that should be used as bootstrap nodes.
	Get(ctx context.Context) ([]peer.AddrInfo, error)
}

var _ Bootstrapper = &KubernetesBootstrapper{}

type KubernetesBootstrapper struct {
	cs                      kubernetes.Interface
	initCh                  chan interface{}
	leaderElectionNamespace string
	leaderElectioName       string
	id                      string
	mx                      sync.RWMutex
}

func NewKubernetesBootstrapper(cs kubernetes.Interface, namespace, name string) *KubernetesBootstrapper {
	return &KubernetesBootstrapper{
		leaderElectionNamespace: namespace,
		leaderElectioName:       name,
		cs:                      cs,
		initCh:                  make(chan interface{}),
	}
}

func (bs *KubernetesBootstrapper) Run(ctx context.Context, id string) error {
	lockCfg := resourcelock.ResourceLockConfig{
		Identity: id,
	}
	rl, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		bs.leaderElectionNamespace,
		bs.leaderElectioName,
		bs.cs.CoreV1(),
		bs.cs.CoordinationV1(),
		lockCfg,
	)
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
				case <-bs.initCh:
					break
				default:
					close(bs.initCh)
				}

				bs.mx.Lock()
				defer bs.mx.Unlock()
				bs.id = identity
			},
		},
	}
	leaderelection.RunOrDie(ctx, leCfg)
	return nil
}

func (bs *KubernetesBootstrapper) Get(ctx context.Context) ([]peer.AddrInfo, error) {
	<-bs.initCh
	bs.mx.RLock()
	defer bs.mx.RUnlock()

	addr, err := multiaddr.NewMultiaddr(bs.id)
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}
	return []peer.AddrInfo{*addrInfo}, err
}

var _ Bootstrapper = &HTTPBootstrapper{}

type HTTPBootstrapper struct {
	addr string
	peer string
}

func NewHTTPBootstrapper(addr, peer string) *HTTPBootstrapper {
	return &HTTPBootstrapper{
		addr: addr,
		peer: peer,
	}
}

func (bs *HTTPBootstrapper) Run(ctx context.Context, id string) error {
	g, ctx := errgroup.WithContext(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // ignore
		w.Write([]byte(id))
	})
	srv := http.Server{
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
	resp, err := http.DefaultClient.Get(bs.peer)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	addr, err := multiaddr.NewMultiaddr(string(b))
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}
	return []peer.AddrInfo{*addrInfo}, err
}
