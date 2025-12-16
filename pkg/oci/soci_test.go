package oci

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSOCIMediaType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mediaType string
		expected  bool
	}{
		{
			name:      "SOCI index V1",
			mediaType: MediaTypeSOCIIndexV1,
			expected:  true,
		},
		{
			name:      "SOCI index V2",
			mediaType: MediaTypeSOCIIndexV2,
			expected:  true,
		},
		{
			name:      "SOCI zTOC",
			mediaType: MediaTypeSOCIzTOC,
			expected:  true,
		},
		{
			name:      "SOCI layer",
			mediaType: MediaTypeSOCILayer,
			expected:  true,
		},
		{
			name:      "regular OCI manifest",
			mediaType: "application/vnd.oci.image.manifest.v1+json",
			expected:  false,
		},
		{
			name:      "regular OCI index",
			mediaType: "application/vnd.oci.image.index.v1+json",
			expected:  false,
		},
		{
			name:      "docker manifest",
			mediaType: "application/vnd.docker.distribution.manifest.v2+json",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := IsSOCIMediaType(tt.mediaType)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsSOCIIndexMediaType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mediaType string
		expected  bool
	}{
		{
			name:      "SOCI index V1",
			mediaType: MediaTypeSOCIIndexV1,
			expected:  true,
		},
		{
			name:      "SOCI index V2",
			mediaType: MediaTypeSOCIIndexV2,
			expected:  true,
		},
		{
			name:      "SOCI zTOC",
			mediaType: MediaTypeSOCIzTOC,
			expected:  false,
		},
		{
			name:      "SOCI layer",
			mediaType: MediaTypeSOCILayer,
			expected:  false,
		},
		{
			name:      "regular OCI manifest",
			mediaType: "application/vnd.oci.image.manifest.v1+json",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := IsSOCIIndexMediaType(tt.mediaType)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsManifestsMediatype_WithSOCI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mediaType string
		expected  bool
	}{
		{
			name:      "SOCI index V1 should be treated as manifest",
			mediaType: MediaTypeSOCIIndexV1,
			expected:  true,
		},
		{
			name:      "SOCI index V2 should be treated as manifest",
			mediaType: MediaTypeSOCIIndexV2,
			expected:  true,
		},
		{
			name:      "SOCI zTOC should not be treated as manifest",
			mediaType: MediaTypeSOCIzTOC,
			expected:  false,
		},
		{
			name:      "SOCI layer should not be treated as manifest",
			mediaType: MediaTypeSOCILayer,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := IsManifestsMediatype(tt.mediaType)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestBackwardCompatibility_NonSOCI(t *testing.T) {
	t.Parallel()

	// content that represents a standard Docker V2 manifest
	dockerManifest := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {
			"mediaType": "application/vnd.docker.container.image.v1+json",
			"size": 7023,
			"digest": "sha256:b5b2b2c507a0944348e0303114d8d93aaaa081732b86451d9bce1f432a537bc7"
		},
		"layers": []
	}`

	// content that represents a standard OCI manifest
	ociManifest := `{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size": 7023,
			"digest": "sha256:b5b2b2c507a0944348e0303114d8d93aaaa081732b86451d9bce1f432a537bc7"
		},
		"layers": []
	}`

	tests := []struct {
		name              string
		content           string
		expectedMediaType string
		isSOCI            bool
	}{
		{
			name:              "Standard Docker Manifest",
			content:           dockerManifest,
			expectedMediaType: "application/vnd.docker.distribution.manifest.v2+json",
			isSOCI:            false,
		},
		{
			name:              "Standard OCI Manifest",
			content:           ociManifest,
			expectedMediaType: "application/vnd.oci.image.manifest.v1+json",
			isSOCI:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Verify FingerprintMediaType returns the correct standard type
			mt, err := FingerprintMediaType(strings.NewReader(tt.content))
			require.NoError(t, err)
			require.Equal(t, tt.expectedMediaType, mt)

			// Verify it is NOT classified as SOCI
			require.False(t, IsSOCIMediaType(mt), "Standard image should not be identified as SOCI")

			// Verify it IS classified as a manifest (so Spegel still handles it)
			require.True(t, IsManifestsMediatype(mt), "Standard image should still be treated as a manifest")
		})
	}
}
