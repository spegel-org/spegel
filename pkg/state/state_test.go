package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/kvick-org/pkg/errgroup"

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

		img, err := oci.ParseImage(imageStr, oci.WithDigest(dgst))
		require.NoError(t, err)
		ociStore.AddImage(img)
		imgs = append(imgs, img)

		err = ociStore.Write(&img, ocispec.Descriptor{Digest: dgst, MediaType: "dummy"}, b)
		require.NoError(t, err)
	}

	log := tlog.NewTestLogger(t)
	ctx := logr.NewContext(t.Context(), log)
	ctx, cancel := context.WithCancel(ctx)

	self := routing.Peer{
		Host:      "test",
		Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		Metadata: routing.PeerMetadata{
			RegistryPort: 5000,
		},
	}
	router := routing.NewMemoryRouter(map[string][]routing.Peer{}, self)
	group := errgroup.WithContext(ctx)
	group.Go(func(ctx context.Context) error {
		return Track(ctx, ociStore, router)
	})
	time.Sleep(100 * time.Millisecond)

	// Check that all images are advertised by digest (this should always happen)
	for _, img := range imgs {
		peers, ok := router.Get(img.Digest.String())
		require.TrueT(t, ok, "Image digest %s should be advertised", img.Digest.String())
		require.Len(t, peers, 1)
	}

	// Check that images have been filtered
	for _, img := range imgs {
		tagName, ok := img.TagName()
		if !ok {
			continue
		}
		peers, ok := router.Get(tagName)
		expectedImages := []string{"docker.io/library/ubuntu:latest", "ghcr.io/spegel-org/spegel:v0.0.9", "quay.io/namespace/repo:latest", "localhost:5000/test:latest"}
		shouldBeAdvertised := slices.Contains(expectedImages, tagName)
		if shouldBeAdvertised {
			require.TrueT(t, ok, "Image %s should be advertised", tagName)
			require.Len(t, peers, 1)
		} else {
			require.FalseT(t, ok, "Image %s should NOT be advertised", tagName)
		}
	}

	cancel()
	err := group.Wait()
	require.ErrorIs(t, err, context.Canceled)
}
