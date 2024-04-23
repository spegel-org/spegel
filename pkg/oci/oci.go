package oci

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
)

type UnknownDocument struct {
	MediaType string `json:"mediaType,omitempty"`
}

type Client interface {
	Name() string
	Verify(ctx context.Context) error
	Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error, error)
	ListImages(ctx context.Context) ([]Image, error)
	AllIdentifiers(ctx context.Context, img Image) ([]string, error)
	Resolve(ctx context.Context, ref string) (digest.Digest, error)
	Size(ctx context.Context, dgst digest.Digest) (int64, error)
	GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error)
	GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error)
	// Deprecated: Use GetBlob.
	CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer) error
}
