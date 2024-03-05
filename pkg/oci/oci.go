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
	Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error)
	ListImages(ctx context.Context) ([]Image, error)
	AllIdentifiers(ctx context.Context, img Image) ([]string, error)
	Resolve(ctx context.Context, ref string) (digest.Digest, error)
	Size(ctx context.Context, dgst digest.Digest) (int64, error)
	GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error)
	CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer, bsize int) error
}
