package oci

import (
	"fmt"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestParseImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		image              string
		expectedRepository string
		expectedTag        string
		expectedDigest     digest.Digest
		digestInImage      bool
	}{
		{
			name:               "Latest tag",
			image:              "library/ubuntu:latest",
			digestInImage:      false,
			expectedRepository: "library/ubuntu",
			expectedTag:        "latest",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
		{
			name:               "Only tag",
			image:              "library/alpine:3.18.0",
			digestInImage:      false,
			expectedRepository: "library/alpine",
			expectedTag:        "3.18.0",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
		{
			name:               "Tag and digest",
			image:              "jetstack/cert-manager-controller:3.18.0@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			digestInImage:      true,
			expectedRepository: "jetstack/cert-manager-controller",
			expectedTag:        "3.18.0",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
		{
			name:               "Only digest",
			image:              "fluxcd/helm-controller@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			digestInImage:      true,
			expectedRepository: "fluxcd/helm-controller",
			expectedTag:        "",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
	}
	registries := []string{"docker.io", "quay.io", "ghcr.com", "127.0.0.1"}
	for _, registry := range registries {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s_%s", tt.name, registry), func(t *testing.T) {
				t.Parallel()

				for _, extraDgst := range []string{tt.expectedDigest.String(), ""} {
					img, err := Parse(fmt.Sprintf("%s/%s", registry, tt.image), digest.Digest(extraDgst))
					if !tt.digestInImage && extraDgst == "" {
						require.EqualError(t, err, "image needs to contain a digest")
						continue
					}
					require.NoError(t, err)
					require.Equal(t, registry, img.Registry)
					require.Equal(t, tt.expectedRepository, img.Repository)
					require.Equal(t, tt.expectedTag, img.Tag)
					require.Equal(t, tt.expectedDigest, img.Digest)
				}
			})

		}
	}
}

func TestParseImageDigestDoesNotMatch(t *testing.T) {
	t.Parallel()

	_, err := Parse("quay.io/jetstack/cert-manager-webhook@sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb", digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"))
	require.EqualError(t, err, "invalid digest set does not match parsed digest: quay.io/jetstack/cert-manager-webhook@sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb")
}

func TestParseImageNoTagOrDigest(t *testing.T) {
	t.Parallel()

	_, err := Parse("ghcr.io/spegel-org/spegel", digest.Digest(""))
	require.EqualError(t, err, "image needs to contain a digest")
}
