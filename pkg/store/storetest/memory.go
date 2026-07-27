package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/store"
)

var _ store.Store = &Memory{}

type Content struct {
	MediaType string
	Data      []byte
}

func (c Content) Descriptor() store.Descriptor {
	return store.Descriptor{Digest: c.Digest(), MediaType: c.MediaType, Size: c.Size()}
}

func (c Content) Size() int64 {
	return int64(len(c.Data))
}

func (c Content) Digest() digest.Digest {
	return digest.FromBytes(c.Data)
}

type Memory struct {
	refs  map[string]digest.Digest
	descs map[digest.Digest]store.Descriptor
	blobs map[digest.Digest][]byte
	mx    sync.RWMutex
}

func NewMemory(contents []Content, refs map[string]digest.Digest) *Memory {
	descs := map[digest.Digest]store.Descriptor{}
	blobs := map[digest.Digest][]byte{}
	for _, c := range contents {
		descs[c.Digest()] = c.Descriptor()
		blobs[c.Digest()] = c.Data
	}
	return &Memory{
		refs:  refs,
		descs: descs,
		blobs: blobs,
	}
}

func (m *Memory) Name() string {
	return "memory"
}

func (m *Memory) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	dgst, ok := m.refs[ref]
	if !ok {
		return "", errors.Join(store.ErrNotFound, fmt.Errorf("digsest cant be resolved for ref %s", ref))
	}
	return dgst, nil
}

func (m *Memory) Descriptor(ctx context.Context, dgst digest.Digest) (store.Descriptor, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	desc, ok := m.descs[dgst]
	if !ok {
		return store.Descriptor{}, errors.Join(store.ErrNotFound, fmt.Errorf("size information for digest %s not found", dgst))
	}
	return desc, nil
}

func (m *Memory) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	b, ok := m.blobs[dgst]
	if !ok {
		return nil, errors.Join(store.ErrNotFound, fmt.Errorf("blob with digest %s not found", dgst))
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

func (m *Memory) Subscribe(ctx context.Context) ([]string, <-chan store.Event, error) {
	m.mx.RLock()
	defer m.mx.RUnlock()

	keys := []string{}
	for dgst := range m.descs {
		keys = append(keys, dgst.String())
	}
	return keys, nil, nil
}
