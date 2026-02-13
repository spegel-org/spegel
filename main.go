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
	"regexp"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"github.com/spegel-org/spegel/internal/otelx"

	"github.com/spegel-org/spegel/internal/cleanup"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
	"github.com/spegel-org/spegel/pkg/state"
	"github.com/spegel-org/spegel/pkg/web"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string   `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MirroredRegistries           []string `arg:"--mirrored-registries,env:MIRRORED_REGISTRIES" help:"Registries that are configured to be mirrored, if slice is empty all registires are mirrored."`
	MirrorTargets                []string `arg:"--mirror-targets,env:MIRROR_TARGETS,required" help:"registries that are configured to act as mirrors."`
	ResolveTags                  bool     `arg:"--resolve-tags,env:RESOLVE_TAGS" default:"true" help:"When true Spegel will resolve tags to digests."`
	PrependExisting              bool     `arg:"--prepend-existing,env:PREPEND_EXISTING" default:"false" help:"When true existing mirror configuration will be kept and Spegel will prepend it's configuration."`
}

type BootstrapConfig struct {
	BootstrapKind        string   `arg:"--bootstrap-kind,env:BOOTSTRAP_KIND" help:"Kind of bootsrapper to use."`
	DNSBootstrapDomain   string   `arg:"--dns-bootstrap-domain,env:DNS_BOOTSTRAP_DOMAIN" help:"Domain to use when bootstrapping using DNS."`
	HTTPBootstrapAddr    string   `arg:"--http-bootstrap-addr,env:HTTP_BOOTSTRAP_ADDR" help:"Address to serve for HTTP bootstrap."`
	HTTPBootstrapPeer    string   `arg:"--http-bootstrap-peer,env:HTTP_BOOTSTRAP_PEER" help:"Peer to HTTP bootstrap with."`
	StaticBootstrapPeers []string `arg:"--static-bootstrap-peers,env:STATIC_BOOTSTRAP_PEERS" help:"Static list of peers to bootstrap with."`
}

type RegistryCmd struct {
	BootstrapConfig
	ContainerdRegistryConfigPath string           `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	MetricsAddr                  string           `arg:"--metrics-addr,env:METRICS_ADDR" default:":9090" help:"address to serve metrics."`
	ContainerdSock               string           `arg:"--containerd-sock,env:CONTAINERD_SOCK" default:"/run/containerd/containerd.sock" help:"Endpoint of containerd service."`
	ContainerdNamespace          string           `arg:"--containerd-namespace,env:CONTAINERD_NAMESPACE" default:"k8s.io" help:"Containerd namespace to fetch images from."`
	ContainerdContentPath        string           `arg:"--containerd-content-path,env:CONTAINERD_CONTENT_PATH" default:"/var/lib/containerd/io.containerd.content.v1.content" help:"Path to Containerd content store"`
	DataDir                      string           `arg:"--data-dir,env:DATA_DIR" default:"/var/lib/spegel" help:"Directory where Spegel persists data."`
	RouterAddr                   string           `arg:"--router-addr,env:ROUTER_ADDR" default:":5001" help:"address to serve router."`
	RegistryAddr                 string           `arg:"--registry-addr,env:REGISTRY_ADDR" default:":5000" help:"address to server image registry."`
	OtelEndpoint                 string           `arg:"--otel-endpoint,env:OTEL_ENDPOINT" help:"OTEL exporter endpoint (e.g., http://otel-collector:4318)."`
	OtelServiceName              string           `arg:"--otel-service-name,env:OTEL_SERVICE_NAME" default:"spegel" help:"Service name for OTEL traces."`
	OtelSampler                  string           `arg:"--otel-sampler,env:OTEL_SAMPLER" default:"parentbased_always_off" help:"Trace sampler (always_on, always_off, parentbased_always_on, parentbased_always_off, or ratio 0.0-1.0)."`
	MirroredRegistries           []string         `arg:"--mirrored-registries,env:MIRRORED_REGISTRIES" help:"Registries that are configured to be mirrored, if slice is empty all registries are mirrored."`
	RegistryFilters              []*regexp.Regexp `arg:"--registry-filters,env:REGISTRY_FILTERS" help:"Regular expressions to filter out tags/registries, if slice is empty all registries/tags are resolved."`
	MirrorResolveTimeout         time.Duration    `arg:"--mirror-resolve-timeout,env:MIRROR_RESOLVE_TIMEOUT" default:"20ms" help:"Max duration spent finding a mirror."`
	MirrorResolveRetries         int              `arg:"--mirror-resolve-retries,env:MIRROR_RESOLVE_RETRIES" default:"3" help:"Max amount of mirrors to attempt."`
	DebugWebEnabled              bool             `arg:"--debug-web-enabled,env:DEBUG_WEB_ENABLED" default:"true" help:"When true enables debug web page."`
	ResolveLatestTag             bool             `arg:"--resolve-latest-tag,env:RESOLVE_LATEST_TAG" default:"true" help:"When true latest tags will be resolved to digests."`
	OtelInsecure                 bool             `arg:"--otel-insecure,env:OTEL_INSECURE" help:"Use insecure connection for OTEL exporter."`
}

type CleanupCmd struct {
	Addr                         string `arg:"--addr,required,env:ADDR" help:"address to run readiness probe on."`
	ContainerdRegistryConfigPath string `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
}

type CleanupWaitCmd struct {
	ProbeEndpoint string        `arg:"--probe-endpoint,required,env:PROBE_ENDPOINT" help:"endpoint to probe cleanup jobs from."`
	Threshold     int           `arg:"--threshold,env:THRESHOLD" default:"3" help:"amount of consecutive successful probes to consider cleanup done."`
	Period        time.Duration `arg:"--period,env:PERIOD" default:"2s" help:"address to run readiness probe on."`
}

type Arguments struct {
	Configuration *ConfigurationCmd `arg:"subcommand:configuration"`
	Registry      *RegistryCmd      `arg:"subcommand:registry"`
	Cleanup       *CleanupCmd       `arg:"subcommand:cleanup"`
	CleanupWait   *CleanupWaitCmd   `arg:"subcommand:cleanup-wait"`
	LogLevel      slog.Level        `arg:"--log-level,env:LOG_LEVEL" default:"INFO" help:"Minimum log level to output. Value should be DEBUG, INFO, WARN, or ERROR."`
}

func main() {
	os.Exit(runMain())
}

func runMain() int {
	args := &Arguments{}
	arg.MustParse(args)

	opts := slog.HandlerOptions{
		AddSource: true,
		Level:     args.LogLevel,
	}
	handler := slog.NewJSONHandler(os.Stderr, &opts)
	log := logr.FromSlogHandler(handler)
	ctx := logr.NewContext(context.Background(), log)

	if args.Registry != nil {
		cfg := otelx.Config{
			ServiceName: args.Registry.OtelServiceName,
			Endpoint:    args.Registry.OtelEndpoint,
			Sampler:     args.Registry.OtelSampler,
			Insecure:    args.Registry.OtelInsecure,
		}
		shutdown, terr := otelx.Setup(ctx, cfg)
		if terr != nil {
			log.Error(terr, "failed to set up telemetry")
		}
		defer func() {
			if shutdown != nil {
				if err := shutdown(context.Background()); err != nil {
					log.Error(err, "failed to shutdown telemetry")
				}
			}
		}()
	}

	err := run(ctx, args)
	if err != nil {
		log.Error(err, "run exit with error")
		return 1
	}
	log.Info("gracefully shutdown")
	return 0
}

func run(ctx context.Context, args *Arguments) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer cancel()
	switch {
	case args.Configuration != nil:
		return configurationCommand(ctx, args.Configuration)
	case args.Registry != nil:
		return registryCommand(ctx, args.Registry)
	case args.Cleanup != nil:
		return cleanupCommand(ctx, args.Cleanup)
	case args.CleanupWait != nil:
		return cleanupWaitCommand(ctx, args.CleanupWait)
	default:
		return errors.New("unknown subcommand")
	}
}

func configurationCommand(ctx context.Context, args *ConfigurationCmd) error {
	username, password, err := loadBasicAuth()
	if err != nil {
		return err
	}
	err = oci.AddMirrorConfiguration(ctx, args.ContainerdRegistryConfigPath, args.MirroredRegistries, args.MirrorTargets, args.ResolveTags, args.PrependExisting, username, password)
	if err != nil {
		return err
	}
	return nil
}

func registryCommand(ctx context.Context, args *RegistryCmd) error {
	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	username, password, err := loadBasicAuth()
	if err != nil {
		return err
	}
	ociClient, err := oci.NewClient()
	if err != nil {
		return err
	}

	filters := []oci.Filter{}
	regFilter, err := oci.FilterForMirroredRegistries(args.MirroredRegistries)
	if err != nil {
		return err
	}
	if regFilter != nil {
		filters = append(filters, *regFilter)
	}
	for _, r := range args.RegistryFilters {
		filters = append(filters, oci.RegexFilter{Regex: r})
	}

	// OCI Store
	ociStore, err := oci.NewContainerd(ctx, args.ContainerdSock, args.ContainerdNamespace, oci.WithContentPath(args.ContainerdContentPath))
	if err != nil {
		return err
	}
	defer ociStore.Close()

	// Router
	_, registryPort, err := net.SplitHostPort(args.RegistryAddr)
	if err != nil {
		return err
	}
	bootstrapper, err := getBootstrapper(args.BootstrapConfig)
	if err != nil {
		return err
	}
	routerOpts := []routing.P2PRouterOption{
		routing.WithDataDir(args.DataDir),
	}
	router, err := routing.NewP2PRouter(ctx, args.RouterAddr, bootstrapper, registryPort, routerOpts...)
	if err != nil {
		return err
	}
	g.Go(func() error {
		err := router.Run(ctx)
		if err != nil {
			return err
		}
		return nil
	})

	// State tracking
	g.Go(func() error {
		err := state.Track(ctx, ociStore, router, state.WithRegistryFilters(filters))
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})

	// Registry
	registryOpts := []registry.RegistryOption{
		registry.WithRegistryFilters(filters),
		registry.WithResolveTimeout(args.MirrorResolveTimeout),
		registry.WithBasicAuth(username, password),
		registry.WithOCIClient(ociClient),
	}
	reg, err := registry.NewRegistry(ociStore, router, registryOpts...)
	if err != nil {
		return err
	}
	regSrv := &http.Server{
		Addr:    args.RegistryAddr,
		Handler: httpx.WrapHandler("registry", reg.Handler(log)),
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

	// Metrics, pprof, and debug web
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
		webOpts := []web.WebOption{
			web.WithOCIClient(ociClient),
			web.WithRegistryFilters(filters),
		}
		mirror := &url.URL{
			Scheme: "http",
			Host:   args.RegistryAddr,
		}
		web, err := web.NewWeb(router, ociStore, reg, mirror, webOpts...)
		if err != nil {
			return err
		}
		mux.Handle("/debug/web/", web.Handler(log))
	}
	metricsSrv := &http.Server{
		Addr:    args.MetricsAddr,
		Handler: httpx.WrapHandler("metrics", mux),
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

func cleanupCommand(ctx context.Context, args *CleanupCmd) error {
	err := cleanup.Run(ctx, args.Addr, args.ContainerdRegistryConfigPath)
	if err != nil {
		return err
	}
	return nil
}

func cleanupWaitCommand(ctx context.Context, args *CleanupWaitCmd) error {
	err := cleanup.Wait(ctx, args.ProbeEndpoint, args.Period, args.Threshold)
	if err != nil {
		return err
	}
	return nil
}

func getBootstrapper(cfg BootstrapConfig) (routing.Bootstrapper, error) { //nolint: ireturn // Return type can be different structs.
	switch cfg.BootstrapKind {
	case "dns":
		return routing.NewDNSBootstrapper(cfg.DNSBootstrapDomain), nil
	case "http":
		return routing.NewHTTPBootstrapper(cfg.HTTPBootstrapAddr, cfg.HTTPBootstrapPeer), nil
	case "static":
		return routing.NewStaticBootstrapperFromStrings(cfg.StaticBootstrapPeers)
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
