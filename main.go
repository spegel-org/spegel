package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/containerd/containerd"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/afero"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	pkgkubernetes "github.com/xenitab/pkg/kubernetes"
	"github.com/xenitab/spegel/internal/mirror"
	"github.com/xenitab/spegel/internal/registry"
	"github.com/xenitab/spegel/internal/routing"
	"github.com/xenitab/spegel/internal/state"
)

type arguments struct {
	Registries                   []url.URL `arg:"--registries,required" help:"registries that are configured to be mirrored."`
	MirrorRegistries             []url.URL `arg:"--mirror-registries,required" help:"registries that are configured to act as mirrors."`
	ImageFilter                  string    `arg:"--image-filter" help:"inclusive image name filter."`
	RegistryAddr                 string    `arg:"--registry-addr,required" help:"address to server image registry."`
	RouterAddr                   string    `arg:"--router-addr,required" help:"address to serve router."`
	MetricsAddr                  string    `arg:"--metrics-addr,required" help:"address to serve metrics."`
	ContainerdSock               string    `arg:"--containerd-sock" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace          string    `arg:"--containerd-namespace" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	ContainerdRegistryConfigPath string    `arg:"--containerd-registry-config-path" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	ContainerdMirrorAdd          bool      `arg:"--containerd-mirror-add" default:"true" help:"Will add containerd mirror configuration if true."`
	ContainerdMirrorRemove       bool      `arg:"--containerd-mirror-remove" default:"true" help:"Will remove containerd mirror configuration if true."`
	KubeconfigPath               string    `arg:"--kubeconfig-path" help:"Path to the kubeconfig file."`
	LeaderElectionNamespace      string    `arg:"--leader-election-namespace" default:"spegel" help:"Kubernetes namespace to write leader election data."`
	LeaderElectionName           string    `arg:"--leader-election-name" default:"spegel-leader-election" help:"Name of leader election."`
}

func main() {
	args := &arguments{}
	arg.MustParse(args)

	zapLog, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("who watches the watchmen (%v)?", err))
	}
	log := zapr.NewLogger(zapLog)

	err = run(log, args)
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}
	log.Info("gracefully shutdown")
}

func run(log logr.Logger, args *arguments) (err error) {
	cs, err := pkgkubernetes.GetKubernetesClientset(args.KubeconfigPath)
	if err != nil {
		return err
	}
	containerdClient, err := containerd.New(args.ContainerdSock, containerd.WithDefaultNamespace(args.ContainerdNamespace))
	if err != nil {
		return fmt.Errorf("could not create containerd client: %w", err)
	}
	defer func() {
		err = errors.Join(err, containerdClient.Close())
	}()

	ctx := logr.NewContext(context.Background(), log)
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)

	// It is fine to immediatly write mirror configuration because it should fallback to another nodes Spegel instance.
	fs := afero.NewOsFs()
	if args.ContainerdMirrorAdd {
		err := mirror.AddMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries, args.MirrorRegistries)
		if err != nil {
			return err
		}
	}
	if args.ContainerdMirrorRemove {
		defer func() {
			err = errors.Join(err, mirror.RemoveMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries))
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:    args.MetricsAddr,
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

	bootstrapper := routing.NewKubernetesBootstrapper(cs, args.LeaderElectionNamespace, args.LeaderElectionName)
	router, err := routing.NewP2PRouter(ctx, args.RouterAddr, bootstrapper)
	if err != nil {
		return err
	}
	g.Go(func() error {
		<-ctx.Done()
		return router.Close()
	})
	g.Go(func() error {
		return state.Track(ctx, containerdClient, router, args.Registries, args.ImageFilter)
	})

	reg, err := registry.NewRegistry(ctx, args.RegistryAddr, containerdClient, router)
	if err != nil {
		return err
	}
	g.Go(func() error {
		return reg.ListenAndServe(ctx)
	})
	g.Go(func() error {
		<-ctx.Done()
		return reg.Shutdown()
	})

	log.Info("running registry", "addr", args.RegistryAddr)
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}
