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

	_, err = FingerprintMediaType(strings.NewReader(" "))
	require.ErrorIs(t, err, io.EOF)

	_, err = FingerprintMediaType(strings.NewReader(" { } "))
	require.EqualError(t, err, "could not determine media type")

	_, err = FingerprintMediaType(strings.NewReader("{ }"))
	require.EqualError(t, err, "could not determine media type")

	_, err = FingerprintMediaType(strings.NewReader("{\"unexpected\":\"value\"}"))
	require.EqualError(t, err, "could not determine media type")

	// Test SOCI V1 index with explicit mediaType
	sociIndexV1Explicit := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.amazon.soci.index.v1+json",
		"blobs": []
	}`
	mt, err = FingerprintMediaType(strings.NewReader(sociIndexV1Explicit))
	require.NoError(t, err)
	require.Equal(t, MediaTypeSOCIIndexV1, mt)

	// Test SOCI V2 index with explicit mediaType
	sociIndexV2Explicit := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.amazon.soci.index.v2+json",
		"blobs": []
	}`
	mt, err = FingerprintMediaType(strings.NewReader(sociIndexV2Explicit))
	require.NoError(t, err)
	require.Equal(t, MediaTypeSOCIIndexV2, mt)

	// Test SOCI V1 index as OCI artifact (standard mediaType with SOCI artifactType)
	sociIndexV1OCIArtifact := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.amazon.soci.index.v1+json",
		"config": {
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
			"size": 2
		},
		"layers": []
	}`
	mt, err = FingerprintMediaType(strings.NewReader(sociIndexV1OCIArtifact))
	require.NoError(t, err)
	require.Equal(t, MediaTypeSOCIIndexV1, mt)

	// Test SOCI V2 index as OCI artifact (standard mediaType with SOCI artifactType)
	sociIndexV2OCIArtifact := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.amazon.soci.index.v2+json",
		"config": {
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
			"size": 2
		},
		"layers": []
	}`
	mt, err = FingerprintMediaType(strings.NewReader(sociIndexV2OCIArtifact))
	require.NoError(t, err)
	require.Equal(t, MediaTypeSOCIIndexV2, mt)

	// Test SOCI zTOC with explicit mediaType
	sociZtoc := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.amazon.soci.ztoc.v1+json"
	}`
	mt, err = FingerprintMediaType(strings.NewReader(sociZtoc))
	require.NoError(t, err)
	require.Equal(t, MediaTypeSOCIzTOC, mt)
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
