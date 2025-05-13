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

		rw.Header().Set(ContentTypeHeader, mt)
		dgst := digest.SHA256.FromBytes(b)
		rw.Header().Set(DigestHeader, dgst.String())
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
