package oci

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/opencontainers/go-digest"
)

var _ Client = &LocalClient{}

type LocalClient struct {
	mx           sync.RWMutex
	rootDir      string
	imageEventCh chan ImageEvent
	errCh        chan error
}

func NewLocalClient(rootDir string) *LocalClient {
	return &LocalClient{
		rootDir: rootDir,
	}
}

func (m *LocalClient) Add() error {
	return nil
}

func (m *LocalClient) Name() string {
	return "local"
}

func (m *LocalClient) Verify(ctx context.Context) error {
	_, err := os.Stat(m.rootDir)
	if err != nil {
		return err
	}
	return nil
}

func (m *LocalClient) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error, error) {
	return nil, nil, nil
}

func (m *LocalClient) ListImages(ctx context.Context) ([]Image, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	return nil, nil
}

func (m *LocalClient) AllIdentifiers(ctx context.Context, img Image) ([]string, error) {
	return []string{img.Digest.String()}, nil
}

func (m *LocalClient) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	return "", nil
}

func (m *LocalClient) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	p := m.pathForDigest(dgst)
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (m *LocalClient) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	p := m.pathForDigest(dgst)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	return b, "", nil
}

func (m *LocalClient) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	p := m.pathForDigest(dgst)
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (m *LocalClient) pathForDigest(dgst digest.Digest) string {
	return filepath.Join(m.rootDir, "blobs", dgst.Algorithm().String(), dgst.Encoded())
}
