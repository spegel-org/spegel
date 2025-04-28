package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	"github.com/spegel-org/spegel/internal/channel"
	"github.com/spegel-org/spegel/pkg/oci"
)

func Run(ctx context.Context, addr, configPath string) error {
	log := logr.FromContextOrDiscard(ctx)

	err := oci.CleanupMirrorConfiguration(ctx, configPath)
	if err != nil {
		return err
	}

	g, gCtx := errgroup.WithContext(ctx)

	mux := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.URL.Path != "/healthz" {
			log.Error(errors.New("unknown request"), "unsupported probe request", "path", req.URL.Path, "method", req.Method)
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	g.Go(func() error {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	log.Info("waiting to be shutdown")
	err = g.Wait()
	if err != nil {
		return err
	}
	return nil
}

func Wait(ctx context.Context, probeEndpoint string, period time.Duration, threshold int) error {
	log := logr.FromContextOrDiscard(ctx)
	resolver := &net.Resolver{}
	client := &http.Client{}

	addr, port, err := net.SplitHostPort(probeEndpoint)
	if err != nil {
		return err
	}

	immediateCh := make(chan time.Time, 1)
	immediateCh <- time.Now()
	close(immediateCh)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	tickerCh := channel.Merge(immediateCh, ticker.C)
	thresholdCount := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tickerCh:
			start := time.Now()

			log.Info("running probe lookup", "host", addr)
			ips, err := resolver.LookupIPAddr(ctx, addr)
			if err != nil {
				log.Error(err, "cleanup probe lookup failed")
				thresholdCount = 0
				continue
			}

			log.Info("running probe request", "endpoints", len(ips))
			err = probeIPs(ctx, client, ips, port)
			if err != nil {
				log.Error(err, "cleanup probe request failed")
				thresholdCount = 0
				continue
			}

			thresholdCount += 1
			log.Info("probe ran successfully", "threshold", thresholdCount, "duration", time.Since(start).String())
			if thresholdCount == threshold {
				log.Info("probe threshold reached")
				return nil
			}
		}
	}
}

func probeIPs(ctx context.Context, client *http.Client, ips []net.IPAddr, port string) error {
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(10)
	for _, ip := range ips {
		g.Go(func() error {
			u := url.URL{
				Scheme: "http",
				Host:   net.JoinHostPort(ip.String(), port),
				Path:   "/healthz",
			}
			reqCtx, cancel := context.WithTimeout(gCtx, 1*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, err = io.Copy(io.Discard, resp.Body)
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("unexpected status code %s", resp.Status)
			}
			return nil
		})
	}
	err := g.Wait()
	if err != nil {
		return err
	}
	return nil
}
