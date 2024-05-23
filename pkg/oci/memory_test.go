package oci

import (
	"testing"

	"github.com/opencontainers/go-digest"
)

func createTestMemory(t *testing.T, imgs []map[string]string, blobs map[digest.Digest][]byte) *MemoryClient {
	images := []Images{}
	return NewMemoryClient(images)
}
