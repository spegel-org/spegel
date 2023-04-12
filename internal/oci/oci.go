package oci

import (
	"context"
	"io"

	digest "github.com/opencontainers/go-digest"
)

type OCIClient interface {
	ResolveTag(ctx context.Context, tag string) (digest.Digest, error)
	GetContent(ctx context.Context, dgst digest.Digest) ([]byte, string, error)
	GetSize(ctx context.Context, dgst digest.Digest) (int64, error)
	Copy(ctx context.Context, dgst digest.Digest, cw io.Writer) error
	ImageDigests(ctx context.Context, dgst digest.Digest) ([]string, error)
	ImageEvents(ctx context.Context) chan string
}