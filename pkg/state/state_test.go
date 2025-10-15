package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"math/rand/v2"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

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
		err = ociStore.Write(ocispec.Descriptor{Digest: dgst, MediaType: "dummy"}, b)
		require.NoError(t, err)
		img, err := oci.ParseImage(imageStr, oci.WithDigest(dgst))
		require.NoError(t, err)
		ociStore.AddImage(img)

		imgs = append(imgs, img)
	}

	tests := []struct {
		name            string
		registryFilters []oci.Filter
		expectedImages  []string
	}{
		{
			name:            "no filters",
			registryFilters: []oci.Filter{},
			expectedImages:  []string{"docker.io/library/ubuntu:latest", "ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter docker.io only",
			registryFilters: []oci.Filter{oci.RegexFilter{Regex: regexp.MustCompile(`^docker\.io/`)}},
			expectedImages:  []string{"ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter multiple registries",
			registryFilters: []oci.Filter{oci.RegexFilter{Regex: regexp.MustCompile(`^docker\.io/`)}, oci.RegexFilter{Regex: regexp.MustCompile(`^ghcr\.io/`)}},
			expectedImages:  []string{"quay.io/namespace/repo:latest", "localhost:5000/test:latest"},
		},
		{
			name:            "filter latest tags",
			registryFilters: []oci.Filter{oci.RegexFilter{Regex: regexp.MustCompile(`:latest$`)}},
			expectedImages:  []string{"ghcr.io/spegel-org/spegel:v0.0.9"},
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
				return Track(gCtx, ociStore, router, WithRegistryFilters(tt.registryFilters))
			})
			time.Sleep(100 * time.Millisecond)

			// Check that all images are advertised by digest (this should always happen)
			for _, img := range imgs {
				peers, ok := router.Get(img.Digest.String())
				require.True(t, ok, "Image digest %s should be advertised", img.Digest.String())
				require.Len(t, peers, 1)
			}

			// Check that images have been filtered
			for _, img := range imgs {
				tagName, ok := img.TagName()
				if !ok {
					continue
				}
				peers, ok := router.Get(tagName)
				shouldBeAdvertised := slices.Contains(tt.expectedImages, tagName)
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
