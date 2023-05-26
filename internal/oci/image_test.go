package oci

import (
	"fmt"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestParseImage(t *testing.T) {
	tests := []struct {
		name               string
		image              string
		expectedRepository string
		expectedTag        string
		expectedDigest     digest.Digest
	}{
		{
			name:               "Latest tag",
			image:              "library/ubuntu:latest",
			expectedRepository: "library/ubuntu",
			expectedTag:        "latest",
			expectedDigest:     "",
		},
		{
			name:               "Only tag",
			image:              "library/alpine:3.18.0",
			expectedRepository: "library/alpine",
			expectedTag:        "3.18.0",
			expectedDigest:     "",
		},
		{
			name:               "Tag and digest",
			image:              "jetstack/cert-manager-controller:3.18.0@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			expectedRepository: "jetstack/cert-manager-controller",
			expectedTag:        "3.18.0",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
		{
			name:               "Only digest",
			image:              "fluxcd/helm-controller@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			expectedRepository: "fluxcd/helm-controller",
			expectedTag:        "",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
		},
	}
	registries := []string{"docker.io", "quay.io", "ghcr.com", "127.0.0.1"}
	for _, registry := range registries {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s_%s", tt.name, registry), func(t *testing.T) {
				img, err := Parse(fmt.Sprintf("%s/%s", registry, tt.image))
				require.NoError(t, err)
				require.Equal(t, registry, img.Registry)
				require.Equal(t, tt.expectedRepository, img.Repository)
				require.Equal(t, tt.expectedTag, img.Tag)
				require.Equal(t, tt.expectedDigest, img.Digest)
			})

		}
	}
}

func TestParseImageNoTagOrDigest(t *testing.T) {
	_, err := Parse("ghcr.io/xenitab/spegel")
	require.EqualError(t, err, "reference needs to contain a tag or digest")
}
