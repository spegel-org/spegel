package containerd

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"
	"github.com/spegel-org/spegel/pkg/oci"
)

func TestIndex(t *testing.T) {
	t.Parallel()

	imgs := map[oci.Image][]digest.Digest{}
	walkFn := func(ctx context.Context, img oci.Image) ([]digest.Digest, error) {
		return imgs[img], nil
	}
	idx := NewIndex(walkFn)

	// Image using digest.
	invalidImg := oci.Image{
		Reference: oci.Reference{
			Registry:   "docker.io",
			Repository: "library/ubuntu",
			Tag:        "latest",
		},
	}
	events, err := idx.Walk(t.Context(), invalidImg)
	require.ErrorIs(t, err, MissingDigestErr)
	require.EqualError(t, err, "image needs digest to be indexed")
	require.Nil(t, events)

	// Latest tag that gets updated.
	latestV1Img, latestV1Dgsts := randomImage(t, "docker.io", "foo/bar", "latest", 6)
	imgs[latestV1Img] = latestV1Dgsts
	events, err = idx.Walk(t.Context(), latestV1Img)
	require.NoError(t, err)
	require.Len(t, idx.imageTagIdx, 1)
	require.Len(t, idx.imageContentIdx, 1)
	require.Len(t, idx.contentRefCount, 6)
	require.Len(t, events, 6)
	for _, count := range idx.contentRefCount {
		require.EqualT(t, count, 1)
	}
	events, err = idx.Walk(t.Context(), latestV1Img)
	require.NoError(t, err)
	require.Empty(t, events)
	require.Len(t, idx.imageTagIdx, 1)
	require.Len(t, idx.imageContentIdx, 1)
	require.Len(t, idx.contentRefCount, 6)
	for _, count := range idx.contentRefCount {
		require.EqualT(t, count, 1)
	}

	latestV2Img, latestV2Dgsts := randomImage(t, "docker.io", "foo/bar", "latest", 5)
	latestV2Dgsts = append(latestV2Dgsts, latestV1Dgsts[len(latestV1Dgsts)-1])
	imgs[latestV2Img] = latestV2Dgsts
	events, err = idx.Walk(t.Context(), latestV2Img)
	require.NoError(t, err)
	// require.Len(t, events, 5)

	// Removing updated does not remove old image.
	events, err = idx.Remove(t.Context(), latestV2Img)
	require.NoError(t, err)

	// Removing old image clears index.
	events, err = idx.Remove(t.Context(), latestV1Img)
	require.NoError(t, err)
	require.Empty(t, idx.imageTagIdx)
	require.Empty(t, idx.imageContentIdx)
	require.Empty(t, idx.contentRefCount)
}

func randomImage(t *testing.T, registry, repository, tag string, layers int) (oci.Image, []digest.Digest) {
	t.Helper()

	img := oci.Image{
		Reference: oci.Reference{
			Registry:   registry,
			Repository: repository,
			Tag:        tag,
		},
	}
	dgsts := []digest.Digest{}
	for range layers {
		dgsts = append(dgsts, digest.FromString(fmt.Sprintf("%d", rand.Int())))
	}
	img.Digest = dgsts[0]
	return img, dgsts
}
