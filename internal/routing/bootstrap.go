package routing

import (
	"context"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type Bootstrapper interface {
	GetAddress(ctx context.Context, id string) (string, error)
}

type KubernetesBootstrapper struct {
	leaderElectionNamespace string
	leaderElectioName       string
	cs                      kubernetes.Interface
}

func NewKubernetesBootstrapper(cs kubernetes.Interface, namespace, name string) Bootstrapper {
	return &KubernetesBootstrapper{
		leaderElectionNamespace: namespace,
		leaderElectioName:       name,
		cs:                      cs,
	}
}

// TODO: Refactor this mess, there should be a simpler solution which runs serial out of the box.
func (k *KubernetesBootstrapper) GetAddress(ctx context.Context, id string) (string, error) {
	lockCfg := resourcelock.ResourceLockConfig{
		Identity: id,
	}
	rl, err := resourcelock.New(resourcelock.ConfigMapsLeasesResourceLock, k.leaderElectionNamespace, k.leaderElectioName, k.cs.CoreV1(), k.cs.CoordinationV1(), lockCfg)
	if err != nil {
		return "", err
	}
	ch := make(chan string)
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
				select {
				case ch <- identity:
					close(ch)
				default:
				}
			},
		},
	}
	go leaderelection.RunOrDie(ctx, leCfg)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case addr := <-ch:
			return addr, nil

		}
	}
}
