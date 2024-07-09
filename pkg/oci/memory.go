package oci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/opencontainers/go-digest"
)

var _ Client = &Memory{}

type Memory struct {
	blobs  map[digest.Digest][]byte
	tags   map[string]digest.Digest
	images []Image
	mx     sync.RWMutex
}

func NewMemory() *Memory {
	return &Memory{
		images: []Image{},
		tags:   map[string]digest.Digest{},
		blobs:  map[digest.Digest][]byte{},
	}
}

func (m *Memory) Name() string {
	return "memory"
}

func (m *Memory) Verify(ctx context.Context) error {
	return nil
}

func (m *Memory) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error, error) {
	return nil, nil, nil
}

func (m *Memory) ListImages(ctx context.Context) ([]Image, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	return m.images, nil
}

func (m *Memory) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	dgst, ok := m.tags[ref]
	if !ok {
		return "", fmt.Errorf("could not resolve tag %s to a digest", ref)
	}
	return dgst, nil
}

func (m *Memory) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	b, ok := m.blobs[dgst]
	if !ok {
		return 0, errors.Join(ErrNotFound, fmt.Errorf("size information for digest %s not found", dgst))
	}
	return int64(len(b)), nil
}

func (m *Memory) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	b, ok := m.blobs[dgst]
	if !ok {
		return nil, "", errors.Join(ErrNotFound, fmt.Errorf("manifest with digest %s not found", dgst))
	}
	mt, err := DetermineMediaType(b)
	if err != nil {
		return nil, "", err
	}
	return b, mt, nil
}

func (m *Memory) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	b, ok := m.blobs[dgst]
	if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("blob with digest %s not found", dgst))
	}
	rc := io.NopCloser(bytes.NewBuffer(b))
	return rc, nil
}

func (m *Memory) AddImage(img Image) {
	m.mx.Lock()
	defer m.mx.Unlock()

	m.images = append(m.images, img)
	tagName, ok := img.TagName()
	if !ok {
		return
	}
	m.tags[tagName] = img.Digest
}

func (m *Memory) AddBlob(b []byte, dgst digest.Digest) {
	m.mx.Lock()
	defer m.mx.Unlock()

	m.blobs[dgst] = b
}
