package oci

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
)

type Client interface {
	Subscribe(ctx context.Context) (<-chan Image, <-chan error)
	ListImages(ctx context.Context) ([]Image, error)
	GetImageDigests(ctx context.Context, img Image) ([]string, error)
	Resolve(ctx context.Context, ref string) (digest.Digest, error)
	GetSize(ctx context.Context, dgst digest.Digest) (int64, error)
	WriteBlob(ctx context.Context, dst io.Writer, dgst digest.Digest) error
	GetBlob(ctx context.Context, dgst digest.Digest) ([]byte, string, error)
}

type UnknownDocument struct {
	MediaType string `json:"mediaType,omitempty"`
}
