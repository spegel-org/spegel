package oci

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
)

var _ Client = &MemoryClient{}

type MemoryClient struct {
	images []Image
}

func NewMemoryClient(images []Image) *MemoryClient {
	return &MemoryClient{
		images: images,
	}
}

func (m *MemoryClient) Name() string {
	return "memory"
}

func (m *MemoryClient) Verify(ctx context.Context) error {
	return nil
}

func (m *MemoryClient) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error, error) {
	return nil, nil, nil
}

func (m *MemoryClient) ListImages(ctx context.Context) ([]Image, error) {
	return m.images, nil
}

func (m *MemoryClient) AllIdentifiers(ctx context.Context, img Image) ([]string, error) {
	return []string{img.Digest.String()}, nil
}

func (m *MemoryClient) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	return "", nil
}

func (m *MemoryClient) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	return 0, nil
}

func (m *MemoryClient) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}

func (m *MemoryClient) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *MemoryClient) CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer) error {
	return nil
}
