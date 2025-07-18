package oci

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/stretchr/testify/require"
)

func TestClient(t *testing.T) {
	t.Parallel()

	img := Image{
		Repository: "test/image",
		Tag:        "latest",
	}

	mem := ocimem.New()
	blobs := []ocispec.Descriptor{
		{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    digest.Digest("sha256:68b8a989a3e08ddbdb3a0077d35c0d0e59c9ecf23d0634584def8bdbb7d6824f"),
			Size:      529,
		},
		{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    digest.Digest("sha256:3caa2469de2a23cbcc209dd0b9d01cd78ff9a0f88741655991d36baede5b0996"),
			Size:      118,
		},
	}
	for _, blob := range blobs {
		f, err := os.Open(filepath.Join("testdata", "blobs", "sha256", blob.Digest.Encoded()))
		require.NoError(t, err)
		_, err = mem.PushBlob(t.Context(), img.Repository, blob, f)
		f.Close()
		require.NoError(t, err)
	}
	manifests := []ocispec.Descriptor{
		{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    digest.Digest("sha256:b6d6089ca6c395fd563c2084f5dd7bc56a2f5e6a81413558c5be0083287a77e9"),
		},
	}
	for _, manifest := range manifests {
		b, err := os.ReadFile(filepath.Join("testdata", "blobs", "sha256", manifest.Digest.Encoded()))
		require.NoError(t, err)
		_, err = mem.PushManifest(t.Context(), img.Repository, img.Tag, b, manifest.MediaType)
		require.NoError(t, err)
	}
	reg := ociserver.New(mem, nil)
	srv := httptest.NewServer(reg)
	t.Cleanup(func() {
		srv.Close()
	})

	client := NewClient(srv.Client())
	mirror, err := url.Parse(srv.URL)
	require.NoError(t, err)
	pullResults, err := client.Pull(t.Context(), img, WithFetchMirror(mirror))
	require.NoError(t, err)
	require.Len(t, pullResults, 3)

	dist := DistributionPath{
		Kind:   DistributionKindBlob,
		Name:   img.Repository,
		Digest: blobs[0].Digest,
	}
	desc, err := client.Head(t.Context(), dist, WithFetchMirror(mirror))
	require.NoError(t, err)
	require.Equal(t, dist.Digest, desc.Digest)
	require.Equal(t, httpx.ContentTypeBinary, desc.MediaType)
}

func TestDescriptorHeader(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	desc := ocispec.Descriptor{
		MediaType: "foo",
		Size:      909,
		Digest:    digest.Digest("sha256:b6d6089ca6c395fd563c2084f5dd7bc56a2f5e6a81413558c5be0083287a77e9"),
	}

	WriteDescriptorToHeader(desc, header)
	require.Equal(t, "foo", header.Get(httpx.HeaderContentType))
	require.Equal(t, "909", header.Get(httpx.HeaderContentLength))
	require.Equal(t, "sha256:b6d6089ca6c395fd563c2084f5dd7bc56a2f5e6a81413558c5be0083287a77e9", header.Get(HeaderDockerDigest))
	headerDesc, err := DescriptorFromHeader(header)
	require.NoError(t, err)
	require.Equal(t, desc, headerDesc)

	header = http.Header{}
	_, err = DescriptorFromHeader(header)
	require.EqualError(t, err, "content type cannot be empty")
	header.Set(httpx.HeaderContentType, "test")
	_, err = DescriptorFromHeader(header)
	require.EqualError(t, err, "content length cannot be empty")
	header.Set(httpx.HeaderContentLength, "wrong")
	_, err = DescriptorFromHeader(header)
	require.EqualError(t, err, "strconv.ParseInt: parsing \"wrong\": invalid syntax")
	header.Set(httpx.HeaderContentLength, "250000")
	_, err = DescriptorFromHeader(header)
	require.EqualError(t, err, "invalid checksum digest format")
	header.Set(HeaderDockerDigest, "foobar")
	_, err = DescriptorFromHeader(header)
	require.EqualError(t, err, "invalid checksum digest format")
}
