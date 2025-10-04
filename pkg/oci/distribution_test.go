package oci

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestParseDistributionPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		registry            string
		path                string
		expectedName        string
		expectedDgst        digest.Digest
		expectedTag         string
		expectedRef         string
		expectedKind        DistributionKind
		execptedIsLatestTag bool
	}{
		{
			name:                "manifest tag",
			registry:            "example.com",
			path:                "/v2/foo/bar/manifests/hello-world",
			expectedName:        "foo/bar",
			expectedDgst:        "",
			expectedTag:         "hello-world",
			expectedRef:         "example.com/foo/bar:hello-world",
			expectedKind:        DistributionKindManifest,
			execptedIsLatestTag: false,
		},
		{
			name:                "manifest with latest tag",
			registry:            "example.com",
			path:                "/v2/test/manifests/latest",
			expectedName:        "test",
			expectedDgst:        "",
			expectedTag:         "latest",
			expectedRef:         "example.com/test:latest",
			expectedKind:        DistributionKindManifest,
			execptedIsLatestTag: true,
		},
		{
			name:                "manifest digest",
			registry:            "docker.io",
			path:                "/v2/library/nginx/manifests/sha256:0a404ca8e119d061cdb2dceee824c914cdc69b31bc7b5956ef5a520436a80d39",
			expectedName:        "library/nginx",
			expectedDgst:        digest.Digest("sha256:0a404ca8e119d061cdb2dceee824c914cdc69b31bc7b5956ef5a520436a80d39"),
			expectedTag:         "",
			expectedRef:         "sha256:0a404ca8e119d061cdb2dceee824c914cdc69b31bc7b5956ef5a520436a80d39",
			expectedKind:        DistributionKindManifest,
			execptedIsLatestTag: false,
		},
		{
			name:                "blob digest",
			registry:            "docker.io",
			path:                "/v2/library/nginx/blobs/sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369",
			expectedName:        "library/nginx",
			expectedDgst:        digest.Digest("sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369"),
			expectedTag:         "",
			expectedRef:         "sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369",
			expectedKind:        DistributionKindBlob,
			execptedIsLatestTag: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := &url.URL{
				Path:     tt.path,
				RawQuery: fmt.Sprintf("ns=%s", tt.registry),
			}
			dist, err := ParseDistributionPath(u)
			require.NoError(t, err)
			require.Equal(t, tt.expectedName, dist.Repository)
			require.Equal(t, tt.expectedDgst, dist.Digest)
			require.Equal(t, tt.expectedTag, dist.Tag)
			require.Equal(t, tt.expectedRef, dist.Identifier())
			require.Equal(t, tt.expectedKind, dist.Kind)
			require.Equal(t, tt.registry, dist.Registry)
			require.Equal(t, tt.path, dist.URL().Path)
			require.Equal(t, tt.registry, dist.URL().Query().Get("ns"))
			require.Equal(t, tt.execptedIsLatestTag, dist.IsLatestTag())
		})
	}
}

func TestParseDistributionPathErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           *url.URL
		expectedError string
	}{
		{
			name: "invalid path",
			url: &url.URL{
				Path:     "/v2/spegel-org/spegel/v0.0.1",
				RawQuery: "ns=example.com",
			},
			expectedError: "distribution path could not be parsed",
		},
		{
			name: "blob with tag reference",
			url: &url.URL{
				Path:     "/v2/spegel-org/spegel/blobs/v0.0.1",
				RawQuery: "ns=example.com",
			},
			expectedError: "invalid checksum digest format",
		},
		{
			name: "blob with invalid digest",
			url: &url.URL{
				Path:     "/v2/spegel-org/spegel/blobs/sha256:123",
				RawQuery: "ns=example.com",
			},
			expectedError: "invalid checksum digest length",
		},
		{
			name: "manifest tag with missing registry",
			url: &url.URL{
				Path: "/v2/spegel-org/spegel/manifests/v0.0.1",
			},
			expectedError: "registry parameter needs to be set for tag references",
		},
		{
			name: "manifest with invalid digest",
			url: &url.URL{
				Path: "/v2/spegel-org/spegel/manifests/sha253:foobar",
			},
			expectedError: "unsupported digest algorithm",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseDistributionPath(tt.url)
			require.EqualError(t, err, tt.expectedError)
		})
	}
}
