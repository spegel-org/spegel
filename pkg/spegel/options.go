package spegel

import (
	"time"

	"github.com/xenitab/spegel/pkg/bootstrap"
	"github.com/xenitab/spegel/pkg/oci"
)

type Config struct {
	ociClient            oci.Client
	bootstrapper         bootstrap.Bootstrapper
	metricsAddr          string
	registryAddr         string
	routerAddr           string
	localAddr            string
	resolveLatestTag     bool
	mirrorResolveTimeout time.Duration
	mirrorResolveRetries int
}

type Option func(*Config)

func WithOCIClient(ociClient oci.Client) Option {
	return func(c *Config) {
		c.ociClient = ociClient
	}
}

func WithBootstrapper(bootstrapper bootstrap.Bootstrapper) Option {
	return func(c *Config) {
		c.bootstrapper = bootstrapper
	}
}

func WithMetricsAddress(metricsAddr string) Option {
	return func(c *Config) {
		c.metricsAddr = metricsAddr
	}
}

func WithRegistryAddress(registryAddr string) Option {
	return func(c *Config) {
		c.registryAddr = registryAddr
	}
}

func WithRouterAddress(routerAddr string) Option {
	return func(c *Config) {
		c.routerAddr = routerAddr
	}
}

func WithLocalAddress(localAddr string) Option {
	return func(c *Config) {
		c.localAddr = localAddr
	}
}

func WithResolveLatestTag(resolveLatestTag bool) Option {
	return func(c *Config) {
		c.resolveLatestTag = resolveLatestTag
	}
}

func WithMirrorResolveRetries(mirrorResolveRetries int) Option {
	return func(c *Config) {
		c.mirrorResolveRetries = mirrorResolveRetries
	}
}

func WithMirrorResolveTimeout(mirrorResolveTimeout time.Duration) Option {
	return func(c *Config) {
		c.mirrorResolveTimeout = mirrorResolveTimeout
	}
}
