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

	"github.com/xenitab/spegel/internal/oci"
	"github.com/xenitab/spegel/internal/registry"
	"github.com/xenitab/spegel/internal/routing"
	"github.com/xenitab/spegel/internal/state"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string    `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	Registries                   []url.URL `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	MirrorRegistries             []url.URL `arg:"--mirror-registries,required" help:"registries that are configured to act as mirrors."`
}

type RegistryCmd struct {
	RegistryAddr            string        `arg:"--registry-addr,required" help:"address to server image registry."`
	RouterAddr              string        `arg:"--router-addr,required" help:"address to serve router."`
	MetricsAddr             string        `arg:"--metrics-addr,required" help:"address to serve metrics."`
	Registries              []url.URL     `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	ContainerdSock          string        `arg:"--containerd-sock" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace     string        `arg:"--containerd-namespace" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	MirrorResolveRetries    int           `arg:"--mirror-resolve-retries" default:"3" help:"Max ammount of mirrors to attempt."`
	MirrorResolveTimeout    time.Duration `arg:"--mirror-resolve-timeout" default:"5s" help:"Max duration spent finding a mirror."`
	KubeconfigPath          string        `arg:"--kubeconfig-path" help:"Path to the kubeconfig file."`
	LeaderElectionNamespace string        `arg:"--leader-election-namespace" default:"spegel" help:"Kubernetes namespace to write leader election data."`
	LeaderElectionName      string        `arg:"--leader-election-name" default:"spegel-leader-election" help:"Name of leader election."`
	ResolveLatestTag        bool          `arg:"--resolve-latest-tag" default:"true" help:"When true latest tags will be resolved to digests."`
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
	err := oci.AddMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries, args.MirrorRegistries)
	if err != nil {
		return err
	}
	return nil
}

func registryCommand(ctx context.Context, args *RegistryCmd) (err error) {
	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	cs, err := pkgkubernetes.GetKubernetesClientset(args.KubeconfigPath)
	if err != nil {
		return err
	}
	ociClient, err := oci.NewContainerd(args.ContainerdSock, args.ContainerdNamespace, args.Registries)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
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

	_, registryPort, err := net.SplitHostPort(args.RegistryAddr)
	if err != nil {
		return err
	}
	bootstrapper := routing.NewKubernetesBootstrapper(cs, args.LeaderElectionNamespace, args.LeaderElectionName)
	router, err := routing.NewP2PRouter(ctx, args.RouterAddr, bootstrapper, registryPort)
	if err != nil {
		return err
	}
	g.Go(func() error {
		<-ctx.Done()
		return router.Close()
	})
	g.Go(func() error {
		return state.Track(ctx, ociClient, router, args.ResolveLatestTag)
	})

	reg := registry.NewRegistry(ociClient, router, args.MirrorResolveRetries, args.MirrorResolveTimeout, args.ResolveLatestTag)
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

	log.Info("running registry", "addr", args.RegistryAddr)
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}
