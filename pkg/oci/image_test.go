package oci

import (
	"fmt"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestParseImageStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		image              string
		expectedRepository string
		expectedTag        string
		expectedString     string
		expectedDigest     digest.Digest
		digestInImage      bool
	}{
		{
			name:               "Only tag",
			image:              "library/alpine:3.18.0",
			digestInImage:      false,
			expectedRepository: "library/alpine",
			expectedTag:        "3.18.0",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
			expectedString:     "library/alpine:3.18.0@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
		},
		{
			name:               "Tag and digest",
			image:              "jetstack/cert-manager-controller:3.18.0@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			digestInImage:      true,
			expectedRepository: "jetstack/cert-manager-controller",
			expectedTag:        "3.18.0",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
			expectedString:     "jetstack/cert-manager-controller:3.18.0@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
		},
		{
			name:               "Only digest",
			image:              "fluxcd/helm-controller@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
			digestInImage:      true,
			expectedRepository: "fluxcd/helm-controller",
			expectedTag:        "",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
			expectedString:     "fluxcd/helm-controller@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
		},
		{
			name:               "Digest only in extra digest",
			image:              "foo/bar",
			digestInImage:      false,
			expectedRepository: "foo/bar",
			expectedDigest:     digest.Digest("sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda"),
			expectedString:     "foo/bar@sha256:c0669ef34cdc14332c0f1ab0c2c01acb91d96014b172f1a76f3a39e63d1f0bda",
		},
	}
	registries := []string{
		"docker.io",
		"quay.io",
		"ghcr.com",
		"example.com:2135",
		"127.0.0.1",
		"127.0.0.1:3",
		"[::1]:8080",
		"[::1]",
		"localhost",
		"localhost:5000",
		"registry:5000",
		"1234.dkr.ecr.eu-west-1.amazonaws.com",
	}
	for _, registry := range registries {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s_%s", tt.name, registry), func(t *testing.T) {
				t.Parallel()

				for _, extraDgst := range []string{tt.expectedDigest.String(), ""} {
					img, err := ParseImage(fmt.Sprintf("%s/%s", registry, tt.image), WithDigest(digest.Digest(extraDgst)))
					if !tt.digestInImage && extraDgst == "" {
						require.EqualError(t, err, fmt.Sprintf("could not parse image %s: image needs to contain a digest", fmt.Sprintf("%s/%s", registry, tt.image)))
						continue
					}
					require.NoError(t, err)
					require.Equal(t, registry, img.Registry)
					require.Equal(t, tt.expectedRepository, img.Repository)
					require.Equal(t, tt.expectedTag, img.Tag)
					require.Equal(t, tt.expectedDigest, img.Digest)
					tagName, ok := img.TagName()
					if tt.expectedTag == "" {
						require.False(t, ok)
						require.Empty(t, tagName)
					} else {
						require.True(t, ok)
						require.Equal(t, registry+"/"+tt.expectedRepository+":"+tt.expectedTag, tagName)
					}
					require.Equal(t, fmt.Sprintf("%s/%s", registry, tt.expectedString), img.String())
				}
			})
		}
	}
}

func TestParseImageStrictErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		s             string
		dgst          digest.Digest
		expectedError string
	}{
		{
			name:          "digests do not match",
			s:             "quay.io/jetstack/cert-manager-webhook@sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb",
			dgst:          digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"),
			expectedError: "could not parse image quay.io/jetstack/cert-manager-webhook@sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb: set digest sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c does not match parsed digest sha256:13fd9eaadb4e491ef0e1d82de60cb199f5ad2ea5a3f8e0c19fdf31d91175b9cb",
		},
		{
			name:          "no tag or digest",
			s:             "ghcr.io/spegel-org/spegel",
			dgst:          "",
			expectedError: "could not parse image ghcr.io/spegel-org/spegel: image needs to contain a digest",
		},
		{
			name:          "reference contains protocol",
			s:             "https://example.com/test:latest",
			dgst:          "",
			expectedError: "could not parse image https://example.com/test:latest: invalid reference format",
		},
		{
			name:          "unparsable url",
			s:             "example%#$.com/foo",
			dgst:          "",
			expectedError: "could not parse image example%#$.com/foo: parse \"//example%\": invalid URL escape \"%\"",
		},
		{
			name:          "missing registry",
			s:             "library/ubuntu:latest",
			dgst:          digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"),
			expectedError: "could not parse image library/ubuntu:latest: reference needs to contain a registry",
		},
		{
			name:          "ipv6 registry without brackets",
			s:             "::1/library/ubuntu:latest",
			dgst:          digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"),
			expectedError: "could not parse image ::1/library/ubuntu:latest: ip6 address ::1 needs to be encaplsulated in square brackets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseImage(tt.s, WithDigest(tt.dgst))
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestNewImageErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		registry      string
		repository    string
		tag           string
		dgst          digest.Digest
		expectedError string
	}{
		{
			name:          "missing registry",
			registry:      "",
			repository:    "foo/bar",
			tag:           "latest",
			dgst:          digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"),
			expectedError: "reference needs to contain a registry",
		},
		{
			name:          "missing repository",
			registry:      "example.com",
			repository:    "",
			tag:           "latest",
			dgst:          digest.Digest("sha256:ec4306b243d98cce7c3b1f994f2dae660059ef521b2b24588cfdc950bd816d4c"),
			expectedError: "reference needs to contain a repository",
		},
		{
			name:          "invalid digest",
			registry:      "example.com",
			repository:    "foo/bar",
			tag:           "latest",
			dgst:          digest.Digest("test"),
			expectedError: "invalid checksum digest format",
		},
		{
			name:          "missing tag and digest",
			registry:      "example.com",
			repository:    "foo/bar",
			tag:           "",
			dgst:          "",
			expectedError: "either tag or digest has to be set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewImage(tt.registry, tt.repository, tt.tag, tt.dgst)
			require.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestParseImageDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "ubuntu",
			expected: "docker.io/library/ubuntu:latest",
		},
		{
			input:    "ubuntu:18.04",
			expected: "docker.io/library/ubuntu:18.04",
		},
		{
			input:    "library/ubuntu",
			expected: "docker.io/library/ubuntu:latest",
		},
		{
			input:    "docker.io/library/ubuntu",
			expected: "docker.io/library/ubuntu:latest",
		},
		{
			input:    "docker.io/ubuntu",
			expected: "docker.io/library/ubuntu:latest",
		},
		{
			input:    "phillebaba/spegel:test@sha256:08d6a6bec0b8d4f0946b6eb22239d8b4a00edb0674307fdf76ad23f9ae77040b",
			expected: "docker.io/phillebaba/spegel:test@sha256:08d6a6bec0b8d4f0946b6eb22239d8b4a00edb0674307fdf76ad23f9ae77040b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			img, err := ParseImage(tt.input, AllowDefaults(), AllowTagOnly())
			require.NoError(t, err)
			require.Equal(t, tt.expected, img.String())
		})
	}
}
