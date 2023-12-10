package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/spf13/afero"
	pkgkubernetes "github.com/xenitab/pkg/kubernetes"
	"go.uber.org/zap"
	"k8s.io/klog/v2"

	"github.com/xenitab/spegel/pkg/bootstrap"
	"github.com/xenitab/spegel/pkg/oci"
	"github.com/xenitab/spegel/pkg/spegel"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string    `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	Registries                   []url.URL `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	MirrorRegistries             []url.URL `arg:"--mirror-registries,required" help:"registries that are configured to act as mirrors."`
	ResolveTags                  bool      `arg:"--resolve-tags" default:"true" help:"When true Spegel will resolve tags to digests."`
}

type RegistryCmd struct {
	RegistryAddr                 string        `arg:"--registry-addr,required" help:"address to server image registry."`
	RouterAddr                   string        `arg:"--router-addr,required" help:"address to serve router."`
	MetricsAddr                  string        `arg:"--metrics-addr,required" help:"address to serve metrics."`
	Registries                   []url.URL     `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	ContainerdSock               string        `arg:"--containerd-sock" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace          string        `arg:"--containerd-namespace" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	ContainerdRegistryConfigPath string        `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MirrorResolveRetries         int           `arg:"--mirror-resolve-retries" default:"3" help:"Max amount of mirrors to attempt."`
	MirrorResolveTimeout         time.Duration `arg:"--mirror-resolve-timeout" default:"5s" help:"Max duration spent finding a mirror."`
	KubeconfigPath               string        `arg:"--kubeconfig-path" help:"Path to the kubeconfig file."`
	LeaderElectionNamespace      string        `arg:"--leader-election-namespace" default:"spegel" help:"Kubernetes namespace to write leader election data."`
	LeaderElectionName           string        `arg:"--leader-election-name" default:"spegel-leader-election" help:"Name of leader election."`
	ResolveLatestTag             bool          `arg:"--resolve-latest-tag" default:"true" help:"When true latest tags will be resolved to digests."`
	LocalAddr                    string        `arg:"--local-addr,required" help:"Address that the local Spegel instance will be reached at."`
}

type Arguments struct {
	Configuration *ConfigurationCmd `arg:"subcommand:configuration"`
	Registry      *RegistryCmd      `arg:"subcommand:registry"`
}

func main() {
	args := &Arguments{}
	arg.MustParse(args)

	zapLog, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("who watches the watchmen (%v)?", err))
	}
	log := zapr.NewLogger(zapLog)
	klog.SetLogger(log)
	ctx := logr.NewContext(context.Background(), log)

	err = run(ctx, args)
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}
	log.Info("gracefully shutdown")
}

func run(ctx context.Context, args *Arguments) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer cancel()
	switch {
	case args.Configuration != nil:
		return configurationCommand(ctx, args.Configuration)
	case args.Registry != nil:
		return registryCommand(ctx, args.Registry)
	default:
		return fmt.Errorf("unknown subcommand")
	}
}

func configurationCommand(ctx context.Context, args *ConfigurationCmd) error {
	fs := afero.NewOsFs()
	err := oci.AddMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries, args.MirrorRegistries, args.ResolveTags)
	if err != nil {
		return err
	}
	return nil
}

func registryCommand(ctx context.Context, args *RegistryCmd) (err error) {
	cs, err := pkgkubernetes.GetKubernetesClientset(args.KubeconfigPath)
	if err != nil {
		return err
	}
	bootstrapper := bootstrap.NewLeaderElectionBootstrapper(cs, args.LeaderElectionNamespace, args.LeaderElectionName)
	ociClient, err := oci.NewContainerd(args.ContainerdSock, args.ContainerdNamespace, args.ContainerdRegistryConfigPath, args.Registries)
	if err != nil {
		return err
	}
	opts := []spegel.Option{
		spegel.WithBootstrapper(bootstrapper),
		spegel.WithOCIClient(ociClient),
		spegel.WithMetricsAddress(args.MetricsAddr),
		spegel.WithRegistryAddress(args.RegistryAddr),
		spegel.WithRouterAddress(args.RouterAddr),
		spegel.WithLocalAddress(args.LocalAddr),
		spegel.WithResolveLatestTag(args.ResolveLatestTag),
		spegel.WithMirrorResolveRetries(args.MirrorResolveRetries),
		spegel.WithMirrorResolveTimeout(args.MirrorResolveTimeout),
	}
	err = spegel.Run(ctx, opts...)
	if err != nil {
		return err
	}
	return nil
}
