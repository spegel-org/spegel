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

// Descriptor describes content stored in the store.
type Descriptor struct {
	// MediaType describes the format of the content.
	MediaType string `json:"mediaType"`

	// Digest identifies the content by its cryptographic digest.
	Digest digest.Digest `json:"digest"`

	// MediaType describes the format of the content.
	Size int64 `json:"size"`
}

// Provider provides access to content stored in the store.
type Provider interface {
	// Name returns the name of the provider.
	Name() string

	// Resolve returns the digest for the given name reference.
	Resolve(ctx context.Context, ref string) (digest.Digest, error)

	// Descriptor returns the descriptor for the given digest.
	Descriptor(ctx context.Context, dgst digest.Digest) (Descriptor, error)

	// Open returns the streamable content for the given digest.
	Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error)
}

type EventType string

const (
	CreateEvent EventType = "CREATE"
	DeleteEvent EventType = "DELETE"
)

// Event describes a change to content in the store.
type Event struct {
	// Type identifies the type of change.
	Type EventType

	// Reference identifies the content affected by the event.
	Reference string

	// Digest identifies the content by its digest.
	Digest digest.Digest
}

// Watcher watches for changes to the store.
type Watcher interface {
	// Watch returns events representing changes in the store.
	Watch(ctx context.Context) ([]Event, <-chan Event, error)
}
