package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	"github.com/spegel-org/spegel/internal/web"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
	"github.com/spegel-org/spegel/pkg/state"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string    `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MirroredRegistries           []url.URL `arg:"--mirrored-registries,env:MIRRORED_REGISTRIES" help:"Registries that are configured to be mirrored, if slice is empty all registires are mirrored."`
	MirrorTargets                []url.URL `arg:"--mirror-targets,env:MIRROR_TARGETS,required" help:"registries that are configured to act as mirrors."`
	ResolveTags                  bool      `arg:"--resolve-tags,env:RESOLVE_TAGS" default:"true" help:"When true Spegel will resolve tags to digests."`
	PrependExisting              bool      `arg:"--prepend-existing,env:PREPEND_EXISTING" default:"false" help:"When true existing mirror configuration will be kept and Spegel will prepend it's configuration."`
}

type BootstrapConfig struct {
	BootstrapKind      string `arg:"--bootstrap-kind,env:BOOTSTRAP_KIND" help:"Kind of bootsrapper to use."`
	DNSBootstrapDomain string `arg:"--dns-bootstrap-domain,env:DNS_BOOTSTRAP_DOMAIN" help:"Domain to use when bootstrapping using DNS."`
	HTTPBootstrapAddr  string `arg:"--http-bootstrap-addr,env:HTTP_BOOTSTRAP_ADDR" help:"Address to serve for HTTP bootstrap."`
	HTTPBootstrapPeer  string `arg:"--http-bootstrap-peer,env:HTTP_BOOTSTRAP_PEER" help:"Peer to HTTP bootstrap with."`
}

type RegistryCmd struct {
	BootstrapConfig
	ContainerdRegistryConfigPath string        `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MetricsAddr                  string        `arg:"--metrics-addr,required,env:METRICS_ADDR" help:"address to serve metrics."`
	LocalAddr                    string        `arg:"--local-addr,required,env:LOCAL_ADDR" help:"Address that the local Spegel instance will be reached at."`
	ContainerdSock               string        `arg:"--containerd-sock,env:CONTAINERD_SOCK" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace          string        `arg:"--containerd-namespace,env:CONTAINERD_NAMESPACE" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	ContainerdContentPath        string        `arg:"--containerd-content-path,env:CONTAINERD_CONTENT_PATH" default:"/var/lib/containerd/io.containerd.content.v1.content" help:"Path to Containerd content store"`
	RouterAddr                   string        `arg:"--router-addr,env:ROUTER_ADDR,required" help:"address to serve router."`
	RegistryAddr                 string        `arg:"--registry-addr,env:REGISTRY_ADDR,required" help:"address to server image registry."`
	Zone                         string        `arg:"--zone,env:ZONE" help:"If set the router will prioritize peers within the same zone."`
	MirroredRegistries           []url.URL     `arg:"--mirrored-registries,env:MIRRORED_REGISTRIES" help:"Registries that are configured to be mirrored, if slice is empty all registires are mirrored."`
	MirrorResolveTimeout         time.Duration `arg:"--mirror-resolve-timeout,env:MIRROR_RESOLVE_TIMEOUT" default:"20ms" help:"Max duration spent finding a mirror."`
	MirrorResolveRetries         int           `arg:"--mirror-resolve-retries,env:MIRROR_RESOLVE_RETRIES" default:"3" help:"Max amount of mirrors to attempt."`
	ResolveLatestTag             bool          `arg:"--resolve-latest-tag,env:RESOLVE_LATEST_TAG" default:"true" help:"When true latest tags will be resolved to digests."`
	DebugWebEnabled              bool          `arg:"--debug-web-enabled,env:DEBUG_WEB_ENABLED" default:"false" help:"When true enables debug web page."`
}

type Arguments struct {
	Configuration *ConfigurationCmd `arg:"subcommand:configuration"`
	Registry      *RegistryCmd      `arg:"subcommand:registry"`
	LogLevel      slog.Level        `arg:"--log-level,env:LOG_LEVEL" default:"INFO" help:"Minimum log level to output. Value should be DEBUG, INFO, WARN, or ERROR."`
}

func main() {
	args := &Arguments{}
	arg.MustParse(args)

	opts := slog.HandlerOptions{
		AddSource: true,
		Level:     args.LogLevel,
	}
	handler := slog.NewJSONHandler(os.Stderr, &opts)
	log := logr.FromSlogHandler(handler)
	klog.SetLogger(log)
	ctx := logr.NewContext(context.Background(), log)

	err := run(ctx, args)
	if err != nil {
		log.Error(err, "run exit with error")
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
		return errors.New("unknown subcommand")
	}
}

func configurationCommand(ctx context.Context, args *ConfigurationCmd) error {
	username, password, err := loadBasicAuth()
	if err != nil {
		return err
	}
	fs := afero.NewOsFs()
	err = oci.AddMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.MirroredRegistries, args.MirrorTargets, args.ResolveTags, args.PrependExisting, username, password)
	if err != nil {
		return err
	}
	return nil
}

func registryCommand(ctx context.Context, args *RegistryCmd) (err error) {
	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	username, password, err := loadBasicAuth()
	if err != nil {
		return err
	}

	// OCI Client
	ociClient, err := oci.NewContainerd(args.ContainerdSock, args.ContainerdNamespace, args.ContainerdRegistryConfigPath, args.MirroredRegistries, oci.WithContentPath(args.ContainerdContentPath))
	if err != nil {
		return err
	}
	err = ociClient.Verify(ctx)
	if err != nil {
		return err
	}

	// Router
	_, registryPort, err := net.SplitHostPort(args.RegistryAddr)
	if err != nil {
		return err
	}
	bootstrapper, err := getBootstrapper(args.BootstrapConfig)
	if err != nil {
		return err
	}

	p2pOpts := []routing.P2PRouterOption{
		routing.Zone(args.Zone),
	}
	router, err := routing.NewP2PRouter(ctx, args.RouterAddr, bootstrapper, registryPort, p2pOpts...)
	if err != nil {
		return err
	}
	g.Go(func() error {
		return router.Run(ctx)
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
		registry.WithLogger(log),
		registry.WithBasicAuth(username, password),
	}
	reg := registry.NewRegistry(ociClient, router, registryOpts...)
	regSrv, err := reg.Server(args.RegistryAddr)
	if err != nil {
		return err
	}
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

	// Metrics
	metrics.Register()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.DefaultGatherer, promhttp.HandlerOpts{}))
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	if args.DebugWebEnabled {
		web, err := web.NewWeb(router)
		if err != nil {
			return err
		}
		mux.Handle("/debug/web/", web.Handler(log))
	}
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

	log.Info("running Spegel", "registry", args.RegistryAddr, "router", args.RouterAddr)
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func getBootstrapper(cfg BootstrapConfig) (routing.Bootstrapper, error) { //nolint: ireturn // Return type can be different structs.
	switch cfg.BootstrapKind {
	case "dns":
		return routing.NewDNSBootstrapper(cfg.DNSBootstrapDomain, 10), nil
	case "http":
		return routing.NewHTTPBootstrapper(cfg.HTTPBootstrapAddr, cfg.HTTPBootstrapPeer), nil
	default:
		return nil, fmt.Errorf("unknown bootstrap kind %s", cfg.BootstrapKind)
	}
}

func loadBasicAuth() (string, string, error) {
	dirPath := "/etc/secrets/basic-auth"
	username, err := os.ReadFile(filepath.Join(dirPath, "username"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	password, err := os.ReadFile(filepath.Join(dirPath, "password"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return string(username), string(password), nil
}
