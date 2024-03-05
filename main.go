package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/afero"
	pkgkubernetes "github.com/xenitab/pkg/kubernetes"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	"github.com/xenitab/spegel/pkg/metrics"
	"github.com/xenitab/spegel/pkg/oci"
	"github.com/xenitab/spegel/pkg/registry"
	"github.com/xenitab/spegel/pkg/routing"
	"github.com/xenitab/spegel/pkg/state"
	"github.com/xenitab/spegel/pkg/throttle"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string    `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	Registries                   []url.URL `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	MirrorRegistries             []url.URL `arg:"--mirror-registries,required" help:"registries that are configured to act as mirrors."`
	ResolveTags                  bool      `arg:"--resolve-tags" default:"true" help:"When true Spegel will resolve tags to digests."`
}

type BootstrapConfig struct {
	BootstrapKind           string `arg:"--bootstrap-kind" help:"Kind of bootsrapper to use."`
	HTTPBootstrapAddr       string `arg:"--http-bootstrap-addr" help:"Address to serve for HTTP bootstrap."`
	HTTPBootstrapPeer       string `àrg:"--http-bootstrap-peer" help:"Peer to HTTP bootstrap with."`
	KubeconfigPath          string `arg:"--kubeconfig-path" help:"Path to the kubeconfig file."`
	LeaderElectionName      string `arg:"--leader-election-name" default:"spegel-leader-election" help:"Name of leader election."`
	LeaderElectionNamespace string `arg:"--leader-election-namespace" default:"spegel" help:"Kubernetes namespace to write leader election data."`
}

type RegistryCmd struct {
	BootstrapConfig
	BlobSpeed                    *throttle.Byterate `arg:"--blob-speed" help:"Maximum write speed per request when serving blob layers. Should be an integer followed by unit Bps, KBps, MBps, GBps, or TBps."`
	ContainerdRegistryConfigPath string             `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MetricsAddr                  string             `arg:"--metrics-addr,required" help:"address to serve metrics."`
	LocalAddr                    string             `arg:"--local-addr,required" help:"Address that the local Spegel instance will be reached at."`
	ContainerdSock               string             `arg:"--containerd-sock" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace          string             `arg:"--containerd-namespace" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	RouterAddr                   string             `arg:"--router-addr,required" help:"address to serve router."`
	RegistryAddr                 string             `arg:"--registry-addr,required" help:"address to server image registry."`
	Registries                   []url.URL          `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	MirrorResolveTimeout         time.Duration      `arg:"--mirror-resolve-timeout" default:"5s" help:"Max duration spent finding a mirror."`
	MirrorResolveRetries         int                `arg:"--mirror-resolve-retries" default:"3" help:"Max amount of mirrors to attempt."`
	ResolveLatestTag             bool               `arg:"--resolve-latest-tag" default:"true" help:"When true latest tags will be resolved to digests."`
	BlobCopyBuffer               int                `arg:"--blob-copy-buffer" default:"32768" help:"IO copy buffer size (bytes) for blob."`
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
	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	// OCI Client
	ociClient, err := oci.NewContainerd(args.ContainerdSock, args.ContainerdNamespace, args.ContainerdRegistryConfigPath, args.Registries)
	if err != nil {
		return err
	}
	err = ociClient.Verify(ctx)
	if err != nil {
		return err
	}

	// Metrics
	metrics.Register()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.DefaultGatherer, promhttp.HandlerOpts{}))
	metricsSrv := &http.Server{
		Addr:    args.MetricsAddr,
		Handler: mux,
	}
	g.Go(func() error {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return metricsSrv.Shutdown(shutdownCtx)
	})

	// Router
	_, registryPort, err := net.SplitHostPort(args.RegistryAddr)
	if err != nil {
		return err
	}
	bootstrapper, err := getBootstrapper(args.BootstrapConfig)
	if err != nil {
		return err
	}
	router, err := routing.NewP2PRouter(ctx, args.RouterAddr, bootstrapper, registryPort)
	if err != nil {
		return err
	}
	g.Go(func() error {
		return router.Run(ctx)
	})
	g.Go(func() error {
		<-ctx.Done()
		return router.Close()
	})

	// State tracking
	g.Go(func() error {
		err := state.Track(ctx, ociClient, router, args.ResolveLatestTag)
		if err != nil {
			return err
		}
		return nil
	})

	// Registry
	registryOpts := []registry.Option{
		registry.WithResolveLatestTag(args.ResolveLatestTag),
		registry.WithResolveRetries(args.MirrorResolveRetries),
		registry.WithResolveTimeout(args.MirrorResolveTimeout),
		registry.WithLocalAddress(args.LocalAddr),
		registry.WithBlobCopyBuffer(args.BlobCopyBuffer),
	}
	if args.BlobSpeed != nil {
		registryOpts = append(registryOpts, registry.WithBlobSpeed(*args.BlobSpeed))
	}
	reg := registry.NewRegistry(ociClient, router, registryOpts...)
	regSrv := reg.Server(args.RegistryAddr, log)
	g.Go(func() error {
		if err := regSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return regSrv.Shutdown(shutdownCtx)
	})

	log.Info("running Spegel", "registry", args.RegistryAddr, "router", args.RouterAddr)
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func getBootstrapper(cfg BootstrapConfig) (routing.Bootstrapper, error) {
	switch cfg.BootstrapKind {
	case "http":
		return routing.NewHTTPBootstrapper(cfg.HTTPBootstrapAddr, cfg.HTTPBootstrapPeer), nil
	case "kubernetes":
		cs, err := pkgkubernetes.GetKubernetesClientset(cfg.KubeconfigPath)
		if err != nil {
			return nil, err
		}
		return routing.NewKubernetesBootstrapper(cs, cfg.LeaderElectionNamespace, cfg.LeaderElectionName), nil
	default:
		return nil, fmt.Errorf("unknown bootstrap kind %s", cfg.BootstrapKind)
	}
}
