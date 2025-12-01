package oci

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestFingerprintMediaType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		dgst              digest.Digest
		expectedMediaType string
	}{
		{
			name:              "image config",
			dgst:              digest.Digest("sha256:68b8a989a3e08ddbdb3a0077d35c0d0e59c9ecf23d0634584def8bdbb7d6824f"),
			expectedMediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:              "image index",
			dgst:              digest.Digest("sha256:9430beb291fa7b96997711fc486bc46133c719631aefdbeebe58dd3489217bfe"),
			expectedMediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:              "image index without media type",
			dgst:              digest.Digest("sha256:d8df04365d06181f037251de953aca85cc16457581a8fc168f4957c978e1008b"),
			expectedMediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:              "image manifest",
			dgst:              digest.Digest("sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53"),
			expectedMediaType: ocispec.MediaTypeImageManifest,
		},
		{
			name:              "image manifest without media type",
			dgst:              digest.Digest("sha256:b6d6089ca6c395fd563c2084f5dd7bc56a2f5e6a81413558c5be0083287a77e9"),
			expectedMediaType: ocispec.MediaTypeImageManifest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := os.Open(filepath.Join("testdata", "blobs", tt.dgst.Algorithm().String(), tt.dgst.Encoded()))
			require.NoError(t, err)
			defer f.Close()
			mt, err := FingerprintMediaType(f)
			require.NoError(t, err)
			require.Equal(t, tt.expectedMediaType, mt)
		})
	}

	mt, err := FingerprintMediaType(strings.NewReader("{}"))
	require.NoError(t, err)
	require.Equal(t, ocispec.MediaTypeEmptyJSON, mt)

	mt, err = FingerprintMediaType(strings.NewReader(" "))
	require.ErrorIs(t, err, io.EOF)

	mt, err = FingerprintMediaType(strings.NewReader(" { } "))
	require.EqualError(t, err, "could not determine media type")

	mt, err = FingerprintMediaType(strings.NewReader("{ }"))
	require.EqualError(t, err, "could not determine media type")

	mt, err = FingerprintMediaType(strings.NewReader("{\"unexpected\":\"value\"}"))
	require.EqualError(t, err, "could not determine media type")
}

func TestIsManifestMediatype(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mt       string
		expected bool
	}{
		{
			mt:       ocispec.MediaTypeImageIndex,
			expected: true,
		},
		{
			mt:       ocispec.MediaTypeImageManifest,
			expected: true,
		},
		{
			mt:       images.MediaTypeDockerSchema2ManifestList,
			expected: true,
		},
		{
			mt:       images.MediaTypeDockerSchema2Manifest,
			expected: true,
		},
		{
			mt:       "foo",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.mt, func(t *testing.T) {
			t.Parallel()

			ok := IsManifestsMediatype(tt.mt)
			require.Equal(t, tt.expected, ok)
		})
	}
}
