package store

import (
	"context"
	"errors"
	"io"

	"github.com/opencontainers/go-digest"
)

var (
	ErrNotFound = errors.New("content not found")
)

type EventType string

const (
	CreateEvent EventType = "CREATE"
	DeleteEvent EventType = "DELETE"
)

type Event struct {
	Type EventType
	Key  string
}

type Descriptor struct {
	MediaType string
	Digest    digest.Digest
	Size      int64
}

type Store interface {
	// Name returns the name of the store implementation.
	Name() string

	// Resolve returns the digest for the reference.
	// Expected format off reference is enforced per implementation.
	Resolve(ctx context.Context, ref string) (digest.Digest, error)

	// Descriptor returns the descriptor for the given digest.
	Descriptor(ctx context.Context, dgst digest.Digest) (Descriptor, error)

	// Open returns the streamable content for the given digest.
	Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error)

	// Subscribe returns an initial state of content  and a channel informing of any changes.
	Subscribe(ctx context.Context) ([]string, <-chan Event, error)
}
