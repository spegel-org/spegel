package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/rand/v2"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestTrack(t *testing.T) {
	t.Parallel()
	ociStore := oci.NewMemory()

	imgRefs := []string{
		"docker.io/library/ubuntu:latest",
		"ghcr.io/spegel-org/spegel:v0.0.9",
		"quay.io/namespace/repo:latest",
		"localhost:5000/test:latest",
	}
	imgs := []oci.Image{}
	for _, imageStr := range imgRefs {
		manifest := ocispec.Manifest{
			Versioned: specs.Versioned{
				SchemaVersion: 2,
			},
			MediaType: ocispec.MediaTypeImageManifest,
			Annotations: map[string]string{
				"random": strconv.Itoa(rand.Int()),
			},
		}
		b, err := json.Marshal(&manifest)
		require.NoError(t, err)
		hash := sha256.New()
		_, err = hash.Write(b)
		require.NoError(t, err)
		dgst := digest.NewDigest(digest.SHA256, hash)
		ociStore.Write(ocispec.Descriptor{Digest: dgst}, b)
		img, err := oci.ParseImageRequireDigest(imageStr, dgst)
		require.NoError(t, err)
		ociStore.AddImage(img)

		imgs = append(imgs, img)
	}

	tests := []struct {
		name            string
		registryFilters []string
		expectedImages  []string // Images that should be advertised
	}{
		{
			name:            "no filters - all images advertised",
			registryFilters: []string{},
			expectedImages:  []string{"docker.io/library/ubuntu:latest", "ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "nil filters - all images advertised",
			registryFilters: nil,
			expectedImages:  []string{"docker.io/library/ubuntu:latest", "ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter docker.io only",
			registryFilters: []string{`^docker\.io/`},
			expectedImages:  []string{"ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter multiple registries",
			registryFilters: []string{`^docker\.io/`, `^ghcr\.io/`},
			expectedImages:  []string{"quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter all registries",
			registryFilters: []string{`^docker\.io/`, `^ghcr\.io/`, `^quay\.io/`, `^localhost:`},
			expectedImages:  []string{},
		},
		{
			name:            "filter with case insensitive pattern",
			registryFilters: []string{`(?i)^docker\.io/`},
			expectedImages:  []string{"ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter with wildcard pattern",
			registryFilters: []string{`.*\.io/`},
			expectedImages:  []string{"localhost:5000/test:latest"},
		},
		{
			name:            "filter with invalid regex - should be ignored",
			registryFilters: []string{`[invalid`, `^docker\.io/`},
			expectedImages:  []string{"ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := tlog.NewTestLogger(t)
			ctx := logr.NewContext(t.Context(), log)
			ctx, cancel := context.WithCancel(ctx)

			router := routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.MustParseAddrPort("127.0.0.1:5000"))
			g, gCtx := errgroup.WithContext(ctx)
			g.Go(func() error {
				return Track(gCtx, ociStore, router, tt.registryFilters)
			})
			time.Sleep(100 * time.Millisecond)

			// Check that all images are advertised by digest (this should always happen)
			for _, img := range imgs {
				peers, ok := router.Lookup(img.Digest.String())
				require.True(t, ok, "Image digest %s should be advertised", img.Digest.String())
				require.Len(t, peers, 1)
			}

			// Check that only expected images are advertised by tag name
			expectedTagNames := make(map[string]bool)
			for _, expectedImg := range tt.expectedImages {
				expectedTagNames[expectedImg] = true
			}

			for _, img := range imgs {
				tagName, ok := img.TagName()
				if !ok {
					continue
				}
				peers, ok := router.Lookup(tagName)
				shouldBeAdvertised := expectedTagNames[tagName]

				if shouldBeAdvertised {
					require.True(t, ok, "Image %s should be advertised", tagName)
					require.Len(t, peers, 1)
				} else {
					require.False(t, ok, "Image %s should NOT be advertised", tagName)
				}
			}

			cancel()
			err := g.Wait()
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}
