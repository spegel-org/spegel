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
	ociClient := oci.NewMemory()

	imgRefs := []string{
		"docker.io/library/ubuntu:latest",
		"ghcr.io/spegel-org/spegel:v0.0.9",
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
		ociClient.AddBlob(b, dgst)
		img, err := oci.ParseImageRequireDigest(imageStr, dgst)
		require.NoError(t, err)
		ociClient.AddImage(img)

		imgs = append(imgs, img)
	}

	tests := []struct {
		name             string
		resolveLatestTag bool
	}{
		{
			name:             "resolve latest",
			resolveLatestTag: true,
		},
		{
			name:             "do not resolve latest",
			resolveLatestTag: false,
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
				return Track(gCtx, ociClient, router, tt.resolveLatestTag)
			})
			time.Sleep(100 * time.Millisecond)

			for _, img := range imgs {
				peers, ok := router.Lookup(img.Digest.String())
				require.True(t, ok)
				require.Len(t, peers, 1)
				tagName, ok := img.TagName()
				if !ok {
					continue
				}
				peers, ok = router.Lookup(tagName)
				if img.IsLatestTag() && !tt.resolveLatestTag {
					require.False(t, ok)
					continue
				}
				require.True(t, ok)
				require.Len(t, peers, 1)
			}

			cancel()
			err := g.Wait()
			require.NoError(t, err)
		})
	}
}
