package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/pkg/oci"
)

func TestMeasureImagePull(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v2/test/image/manifests/index":
			rw.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
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
				rw.WriteHeader(http.StatusInternalServerError)
				return
			}
			//nolint: errcheck // Ignore error.
			rw.Write(b)
		case "/v2/test/image/manifests/manifest":
			rw.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
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
				rw.WriteHeader(http.StatusInternalServerError)
				return
			}
			//nolint: errcheck // Ignore error.
			rw.Write(b)
		case "/v2/test/image/manifests/config":
			rw.Header().Set("Content-Type", ocispec.MediaTypeImageConfig)
			config := ocispec.ImageConfig{
				User: "root",
			}
			b, err := json.Marshal(&config)
			if err != nil {
				rw.WriteHeader(http.StatusInternalServerError)
				return
			}
			//nolint: errcheck // Ignore error.
			rw.Write(b)
		case "/v2/test/image/blobs/layer":
			//nolint: errcheck // Ignore error.
			rw.Write([]byte("Hello World"))
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		srv.Close()
	})

	img := oci.Image{
		Repository: "test/image",
		Digest:     digest.Digest("index"),
		Registry:   "example.com",
	}
	pullResults, err := measureImagePull(srv.Client(), srv.URL, img)
	require.NoError(t, err)

	require.NotEmpty(t, pullResults)
}

func TestFormatByteSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expected string
		size     int64
	}{
		{
			size:     1,
			expected: "1 B",
		},
		{
			size:     18954,
			expected: "19.0 kB",
		},
		{
			size:     1000000000,
			expected: "1.0 GB",
		},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatInt(tt.size, 10), func(t *testing.T) {
			t.Parallel()

			result := formatByteSize(tt.size)
			require.Equal(t, tt.expected, result)
		})
	}
}
