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
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	"github.com/spegel-org/spegel/internal/kubernetes"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
	"github.com/spegel-org/spegel/pkg/state"

	logging "github.com/ipfs/go-log/v2"
)

type ConfigurationCmd struct {
	ContainerdRegistryConfigPath string        `arg:"--containerd-registry-config-path,env:CONTAINERD_REGISTRY_CONFIG_PATH" default:"/etc/containerd/certs.d" help:"Directory where mirror configuration is written."`
	Registries                   []url.URL     `arg:"--registries,required,env:REGISTRIES" help:"registries that are configured to be mirrored."`
	MirrorRegistries             []url.URL     `arg:"--mirror-registries,env:MIRROR_REGISTRIES,required" help:"registries that are configured to act as mirrors."`
	ResolveTags                  bool          `arg:"--resolve-tags,env:RESOLVE_TAGS" default:"true" help:"When true Spegel will resolve tags to digests."`
	AppendMirrors                bool          `arg:"--append-mirrors,env:APPEND_MIRRORS" default:"false" help:"When true existing mirror configuration will be appended to instead of replaced."`
	RegistriesFilePath           string        `arg:"--registries-file-path,env:REGISTRIES_FILE_PATH" help:"Path to the registries file."`
	RefreshInterval              time.Duration `arg:"--refresh-interval,env:REFRESH_INTERVAL" default:"5m" help:"Interval to refresh the mirror configuration."`
}

type BootstrapConfig struct {
	BootstrapKind           string `arg:"--bootstrap-kind,env:BOOTSTRAP_KIND" help:"Kind of bootsrapper to use."`
	KubeconfigPath          string `arg:"--kubeconfig-path,env:KUBECONFIG_PATH" help:"Path to the kubeconfig file."`
	LeaderElectionName      string `arg:"--leader-election-name,env:LEADER_ELECTION_NAME" default:"spegel-leader-election" help:"Name of leader election."`
	LeaderElectionNamespace string `arg:"--leader-election-namespace,env:LEADER_ELECTION_NAMESPACE" default:"spegel" help:"Kubernetes namespace to write leader election data."`
	DNSBootstrapDomain      string `arg:"--dns-bootstrap-domain,env:DNS_BOOTSTRAP_DOMAIN" help:"Domain to use when bootstrapping using DNS."`
	HTTPBootstrapAddr       string `arg:"--http-bootstrap-addr,env:HTTP_BOOTSTRAP_ADDR" help:"Address to serve for HTTP bootstrap."`
	HTTPBootstrapPeer       string `arg:"--http-bootstrap-peer,env:HTTP_BOOTSTRAP_PEER" help:"Peer to HTTP bootstrap with."`
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
	Registries                   []url.URL     `arg:"--registries,env:REGISTRIES" help:"registries that are configured to be mirrored."`
	MirrorResolveTimeout         time.Duration `arg:"--mirror-resolve-timeout,env:MIRROR_RESOLVE_TIMEOUT" default:"20ms" help:"Max duration spent finding a mirror."`
	MirrorResolveRetries         int           `arg:"--mirror-resolve-retries,env:MIRROR_RESOLVE_RETRIES" default:"3" help:"Max amount of mirrors to attempt."`
	ResolveLatestTag             bool          `arg:"--resolve-latest-tag,env:RESOLVE_LATEST_TAG" default:"true" help:"When true latest tags will be resolved to digests."`
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
	fs := afero.NewOsFs()
	err := oci.AddMirrorConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries, args.MirrorRegistries, args.ResolveTags, args.AppendMirrors)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)

	// State tracking
	g.Go(func() error {
		err := state.TrackConfiguration(ctx, fs, args.ContainerdRegistryConfigPath, args.Registries, args.MirrorRegistries, args.ResolveTags, args.RegistriesFilePath, args.RefreshInterval)
		if err != nil {
			return err
		}
		return nil
	})

	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func registryCommand(ctx context.Context, args *RegistryCmd) (err error) {
	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	// enable logging for dht
	logging.SetAllLoggers(logging.LevelInfo)
	// OCI Client
	ociClient, err := oci.NewContainerd(ctx, args.ContainerdSock, args.ContainerdNamespace, args.ContainerdRegistryConfigPath, args.Registries, oci.WithContentPath(args.ContainerdContentPath))
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

	log.Info("running Spegel", "registry", args.RegistryAddr, "router", args.RouterAddr)
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func getBootstrapper(cfg BootstrapConfig) (routing.Bootstrapper, error) { //nolint: ireturn // Return type can be different structs.
	switch cfg.BootstrapKind {
	case "kubernetes":
		cs, err := kubernetes.GetClientset(cfg.KubeconfigPath)
		if err != nil {
			return nil, err
		}
		return routing.NewKubernetesBootstrapper(cs, cfg.LeaderElectionNamespace, cfg.LeaderElectionName), nil
	case "dns":
		return routing.NewDNSBootstrapper(cfg.DNSBootstrapDomain, 10), nil
	case "http":
		return routing.NewHTTPBootstrapper(cfg.HTTPBootstrapAddr, cfg.HTTPBootstrapPeer), nil
	default:
		return nil, fmt.Errorf("unknown bootstrap kind %s", cfg.BootstrapKind)
	}
}
