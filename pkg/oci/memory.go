package oci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var _ Store = &Memory{}

type Memory struct {
	descs  map[digest.Digest]ocispec.Descriptor
	blobs  map[digest.Digest][]byte
	tags   map[string]Image
	images map[Image][]digest.Digest
	mx     sync.RWMutex
}

func NewMemory() *Memory {
	return &Memory{
		images: map[Image][]digest.Digest{},
		tags:   map[string]Image{},
		descs:  map[digest.Digest]ocispec.Descriptor{},
		blobs:  map[digest.Digest][]byte{},
	}
}

func (m *Memory) Name() string {
	return "memory"
}

func (m *Memory) Subscribe(ctx context.Context) (map[Image][]digest.Digest, <-chan OCIEvent, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	return m.images, nil, nil
}

func (m *Memory) ListImages(ctx context.Context) ([]Image, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	imgs := []Image{}
	for img := range m.images {
		imgs = append(imgs, img)
	}
	return imgs, nil
}

func (m *Memory) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	img, ok := m.tags[ref]
	if !ok {
		return "", fmt.Errorf("could not resolve tag %s to a digest", ref)
	}
	return img.Digest, nil
}

func (m *Memory) Descriptor(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	desc, ok := m.descs[dgst]
	if !ok {
		return ocispec.Descriptor{}, errors.Join(ErrNotFound, fmt.Errorf("size information for digest %s not found", dgst))
	}
	return desc, nil
}

func (m *Memory) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	b, ok := m.blobs[dgst]
	if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("blob with digest %s not found", dgst))
	}
	rc := io.NewSectionReader(bytes.NewReader(b), 0, int64(len(b)))
	return struct {
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: rc,
		Closer:     io.NopCloser(nil),
	}, nil
}

func (m *Memory) AddImage(img Image) {
	m.mx.Lock()
	defer m.mx.Unlock()

	tagName, ok := img.TagName()
	if ok {
		m.tags[tagName] = img
	}
	m.images[img] = nil
}

func (m *Memory) Write(img *Image, desc ocispec.Descriptor, b []byte) error {
	if desc.Size == 0 {
		desc.Size = int64(len(b))
	}
	if desc.Size != int64(len(b)) {
		return errors.New("descriptor size and byte size do not match")
	}
	if desc.MediaType == "" {
		return errors.New("media type cannot be empty")
	}
	if desc.Digest == "" {
		return errors.New("digest cannot be empty")
	}
	computedDgst := desc.Digest.Algorithm().FromBytes(b)
	if desc.Digest != computedDgst {
		return fmt.Errorf("computed digest %s does not match given digest %s", computedDgst, desc.Digest)
	}

	m.mx.Lock()
	defer m.mx.Unlock()

	if img != nil {
		dgsts := m.images[*img]
		dgsts = append(dgsts, desc.Digest)
		m.images[*img] = dgsts
	}

	m.descs[desc.Digest] = desc
	m.blobs[desc.Digest] = b
	return nil
}
