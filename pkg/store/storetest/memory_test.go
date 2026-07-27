package storetest

import (
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/httpx"
)

func TestMemoryStore(t *testing.T) {
	t.Parallel()

	contents := []Content{
		{
			MediaType: httpx.ContentTypeBinary,
			Data:      []byte("test"),
		},
	}
	refs := map[string]digest.Digest{
		"existing": contents[0].Digest(),
	}
	s := NewMemory(contents, refs)

	cfg := ConformanceConfig{
		Name:               "memory",
		NotFoundRef:        "dummy",
		ExistingRef:        "existing",
		ExistingRefDigest:  contents[0].Digest(),
		NotFoundDigest:     digest.FromBytes(nil),
		ExistingDescriptor: contents[0].Descriptor(),
	}
	Conformance(t, s, cfg)
}
