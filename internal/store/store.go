package store

import (
	"context"
	"fmt"
	"time"
)

const KeyExpiration = 5 * time.Minute

type Store interface {
	Start() error
	Ready() error
	Stop() error
	Set(ctx context.Context, layers []string) error
	Remove(ctx context.Context, layers []string) error
	Get(ctx context.Context, layer string) ([]string, error)
	Dump(ctx context.Context) ([]string, error)
}

func getKey(ip, layer string) string {
	return fmt.Sprintf("layer:%s:%s", ip, layer)
}
