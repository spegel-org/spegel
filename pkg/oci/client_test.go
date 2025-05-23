package oci

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/stretchr/testify/require"
)

func TestPull(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		b, mt, err := func() ([]byte, string, error) {
			switch req.URL.Path {
			case "/v2/test/image/manifests/index":
				idx := ocispec.Index{
					Manifests: []ocispec.Descriptor{
						{
							Digest: digest.Digest("manifest"),
							Platform: &ocispec.Platform{
								OS:           runtime.GOOS,
								Architecture: runtime.GOARCH,
							},
						},
					},
				}
				b, err := json.Marshal(&idx)
				if err != nil {
					return nil, "", err
				}
				return b, ocispec.MediaTypeImageIndex, nil
			case "/v2/test/image/manifests/manifest":
				manifest := ocispec.Manifest{
					Config: ocispec.Descriptor{
						Digest: digest.Digest("config"),
					},
					Layers: []ocispec.Descriptor{
						{
							Digest: digest.Digest("layer"),
						},
					},
				}
				b, err := json.Marshal(&manifest)
				if err != nil {
					return nil, "", err
				}
				return b, ocispec.MediaTypeImageManifest, nil
			case "/v2/test/image/blobs/config":
				config := ocispec.ImageConfig{
					User: "root",
				}
				b, err := json.Marshal(&config)
				if err != nil {
					return nil, "", err
				}
				return b, ocispec.MediaTypeImageConfig, nil
			case "/v2/test/image/blobs/layer":
				return []byte("hello world"), ocispec.MediaTypeImageLayer, nil
			default:
				return nil, "", errors.New("not found")
			}
		}()
		if err != nil {
			rw.WriteHeader(http.StatusNotFound)
			return
		}

		rw.Header().Set(httpx.HeaderContentType, mt)
		dgst := digest.SHA256.FromBytes(b)
		rw.Header().Set(HeaderDockerDigest, dgst.String())
		rw.WriteHeader(http.StatusOK)

		//nolint: errcheck // Ignore error.
		rw.Write(b)
	}))
	t.Cleanup(func() {
		srv.Close()
	})

	img := Image{
		Repository: "test/image",
		Digest:     digest.Digest("index"),
		Registry:   "example.com",
	}
	client := NewClient()
	pullResults, err := client.Pull(t.Context(), img, srv.URL)
	require.NoError(t, err)

	require.NotEmpty(t, pullResults)
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
