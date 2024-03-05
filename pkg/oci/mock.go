package oci

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
)

var _ Client = &MockClient{}

type MockClient struct {
	images []Image
}

func NewMockClient(images []Image) *MockClient {
	return &MockClient{
		images: images,
	}
}

func (m *MockClient) Name() string {
	return "mock"
}

func (m *MockClient) Verify(ctx context.Context) error {
	return nil
}

func (m *MockClient) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error) {
	return nil, nil
}

func (m *MockClient) ListImages(ctx context.Context) ([]Image, error) {
	return m.images, nil
}

func (m *MockClient) AllIdentifiers(ctx context.Context, img Image) ([]string, error) {
	return []string{img.Digest.String()}, nil
}

func (m *MockClient) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	return "", nil
}

func (m *MockClient) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	return 0, nil
}

func (m *MockClient) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}

func (m *MockClient) CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer, bsize int) error {
	return nil
}
