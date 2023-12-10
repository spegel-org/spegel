package spegel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/xenitab/spegel/internal/registry"
	"github.com/xenitab/spegel/internal/routing"
	"github.com/xenitab/spegel/internal/state"
)

func Run(ctx context.Context, opts ...Option) error {
	cfg := &Config{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.ociClient == nil {
		return fmt.Errorf("oci client cannot be nil")
	}
	if cfg.bootstrapper == nil {
		return fmt.Errorf("boostrapper cannot be nil")
	}

	err := cfg.ociClient.Verify(ctx)
	if err != nil {
		return err
	}

	log := logr.FromContextOrDiscard(ctx)
	g, ctx := errgroup.WithContext(ctx)

	if cfg.metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		metricsSrv := &http.Server{
			Addr:    cfg.metricsAddr,
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
	}

	_, registryPort, err := net.SplitHostPort(cfg.registryAddr)
	if err != nil {
		return err
	}
	router, err := routing.NewP2PRouter(ctx, cfg.routerAddr, cfg.bootstrapper, registryPort)
	if err != nil {
		return err
	}
	g.Go(func() error {
		<-ctx.Done()
		return router.Close()
	})
	g.Go(func() error {
		state.Track(ctx, cfg.ociClient, router, cfg.resolveLatestTag)
		return nil
	})

	reg := registry.NewRegistry(cfg.ociClient, router, cfg.localAddr, cfg.mirrorResolveRetries, cfg.mirrorResolveTimeout, cfg.resolveLatestTag)
	regSrv := reg.Server(cfg.registryAddr, log)
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

	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}
