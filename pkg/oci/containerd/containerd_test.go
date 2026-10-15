package containerd

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/typeurl/v2"
	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

func TestContainerd(t *testing.T) {
	t.Parallel()

	ctrd, err := NewContainerd(t.Context(), "test.sock", "", WithContentPath("foobar"), WithConnection(&net.UnixConn{}))
	require.NoError(t, err)
	require.EqualT(t, "foobar", ctrd.contentPath)

	contentPath := t.TempDir()
	data := []byte("Hello World")
	dgst := digest.FromBytes(data)
	fp := filepath.Join(contentPath, "blobs", dgst.Algorithm().String(), dgst.Encoded())
	err = os.MkdirAll(filepath.Dir(fp), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(fp, data, 0o644)
	require.NoError(t, err)
	ctrd = &Containerd{
		contentPath: contentPath,
	}
	rc, err := ctrd.Open(t.Context(), digest.FromBytes(nil))
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Nil(t, rc)
	rc, err = ctrd.Open(t.Context(), dgst)
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	err = rc.Close()
	require.NoError(t, err)
	require.EqualT(t, dgst, digest.FromBytes(b))
}

func TestDeduplicateImages(t *testing.T) {
	t.Parallel()

	cImgs := []images.Image{
		{
			Name: "docker.io/library/python:3",
			Target: ocispec.Descriptor{
				Digest: digest.Digest("sha256:16e7e5fc4e79fe153c818ac9b46595c7addddaad744d49595377836e300ad1c6"),
			},
		},
		{
			Name: "docker.io/library/ubuntu:25.10",
			Target: ocispec.Descriptor{
				Digest: digest.Digest("sha256:7cc5e35f6567ee8c66d2abb4aab0fd866669e6207c237c3a8f0947a5c7f17092"),
			},
		},
		{
			Name: "docker.io/library/ubuntu",
			Target: ocispec.Descriptor{
				Digest: digest.Digest("sha256:152dc042452c496007f07ca9127571cb9c29697f42acbfad72324b2bb2e43c98"),
			},
		},
		{
			Name: "foobar",
			Target: ocispec.Descriptor{
				Digest: digest.Digest("sha256:aec070645fe53ee3b3763059376134f058cc337247c978add178b6ccdfb0019f"),
			},
		},
		{
			Name: "docker.io/library/ubuntu",
			Target: ocispec.Descriptor{
				Digest: digest.Digest("sha256:7cc5e35f6567ee8c66d2abb4aab0fd866669e6207c237c3a8f0947a5c7f17092"),
			},
		},
	}
	imgs := deduplicateImages(t.Context(), cImgs, nil)
	expected := []oci.Image{
		{
			Reference: oci.Reference{
				Registry:   "docker.io",
				Repository: "library/python",
				Digest:     digest.Digest("sha256:16e7e5fc4e79fe153c818ac9b46595c7addddaad744d49595377836e300ad1c6"),
				Tag:        "3",
			},
		},
		{
			Reference: oci.Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Digest:     digest.Digest("sha256:7cc5e35f6567ee8c66d2abb4aab0fd866669e6207c237c3a8f0947a5c7f17092"),
				Tag:        "25.10",
			},
		},
		{
			Reference: oci.Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Digest:     digest.Digest("sha256:152dc042452c496007f07ca9127571cb9c29697f42acbfad72324b2bb2e43c98"),
			},
		},
	}
	require.ElementsMatch(t, expected, imgs)
}

func TestHandleEvent(t *testing.T) {
	t.Parallel()

	ctrd := Containerd{}
	storeEvts, err := ctrd.handleEvent(t.Context(), events.Envelope{}, nil)
	require.EqualError(t, err, "envelope event cannot be nil")
	require.Empty(t, storeEvts)

	event, err := typeurl.MarshalAny(&eventtypes.ContainerCreate{})
	require.NoError(t, err)
	storeEvts, err = ctrd.handleEvent(t.Context(), events.Envelope{Event: event}, nil)
	require.EqualError(t, err, "unsupported event type *events.ContainerCreate")
	require.Empty(t, storeEvts)
}
