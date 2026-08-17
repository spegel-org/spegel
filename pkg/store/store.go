package store

import (
	"context"

	"github.com/opencontainers/go-digest"
)

type EventType string

const (
	CreateEvent EventType = "CREATE"
	DeleteEvent EventType = "DELETE"
)

type Event struct {
	Type      EventType
	Reference string
	Digest    digest.Digest
}

type Watcher interface {
	Watch(ctx context.Context) ([]Event, <-chan Event, error)
}
