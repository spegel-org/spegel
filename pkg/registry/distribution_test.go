package registry

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		refName  string
		expected bool
	}{
		{
			name:     "with latest tag",
			refName:  "ghcr.io/spegel-org/spegel:latest",
			expected: true,
		},
		{
			name:     "no latest tag",
			refName:  "ghcr.io/spegel-org/spegel:v1.0.0",
			expected: false,
		},
		{
			name:     "empty name",
			refName:  "",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref := reference{
				name: tt.refName,
			}
			require.Equal(t, tt.expected, ref.hasLatestTag())
		})
	}
}

func TestParsePathComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		registry        string
		path            string
		expectedName    string
		expectedDgst    digest.Digest
		expectedRefKind referenceKind
	}{
		{
			name:            "valid manifest tag",
			registry:        "example.com",
			path:            "/v2/foo/bar/manifests/hello-world",
			expectedName:    "example.com/foo/bar:hello-world",
			expectedDgst:    "",
			expectedRefKind: referenceKindManifest,
		},
		{
			name:            "valid manifest digest",
			registry:        "docker.io",
			path:            "/v2/library/nginx/manifests/sha256:0a404ca8e119d061cdb2dceee824c914cdc69b31bc7b5956ef5a520436a80d39",
			expectedName:    "",
			expectedDgst:    digest.Digest("sha256:0a404ca8e119d061cdb2dceee824c914cdc69b31bc7b5956ef5a520436a80d39"),
			expectedRefKind: referenceKindManifest,
		},
		{
			name:            "valid blob digest",
			registry:        "docker.io",
			path:            "/v2/library/nginx/blobs/sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369",
			expectedName:    "",
			expectedDgst:    digest.Digest("sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369"),
			expectedRefKind: referenceKindBlob,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref, err := parsePathComponents(tt.registry, tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.expectedName, ref.name)
			require.Equal(t, tt.expectedDgst, ref.dgst)
			require.Equal(t, tt.expectedRefKind, ref.kind)
		})
	}
}

func TestParsePathComponentsInvalidPath(t *testing.T) {
	t.Parallel()

	_, err := parsePathComponents("example.com", "/v2/spegel-org/spegel/v0.0.1")
	require.EqualError(t, err, "distribution path could not be parsed")
}

func TestParsePathComponentsMissingRegistry(t *testing.T) {
	t.Parallel()

	_, err := parsePathComponents("", "/v2/spegel-org/spegel/manifests/v0.0.1")
	require.EqualError(t, err, "registry parameter needs to be set for tag references")
}
