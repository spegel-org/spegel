package containerd

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"

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

func TestContentLabelsToReferences(t *testing.T) {
	t.Parallel()

	dgst := digest.Digest("foo")
	tests := []struct {
		name     string
		labels   map[string]string
		expected []oci.Reference
	}{
		{
			name: "one matching",
			labels: map[string]string{
				"containerd.io/distribution.source.docker.io": "library/alpine",
			},
			expected: []oci.Reference{
				{
					Registry:   "docker.io",
					Repository: "library/alpine",
					Digest:     dgst,
				},
			},
		},
		{
			name: "multiple matching",
			labels: map[string]string{
				"containerd.io/distribution.source.example.com": "foo",
				"containerd.io/distribution.source.ghcr.io":     "spegel-org/spegel",
			},
			expected: []oci.Reference{
				{
					Registry:   "ghcr.io",
					Repository: "spegel-org/spegel",
					Digest:     dgst,
				},
				{
					Registry:   "example.com",
					Repository: "foo",
					Digest:     dgst,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			t.Parallel()

			refs, err := contentLabelsToReferences(tt.labels, dgst)
			require.NoError(t, err)
			require.ElementsMatchT(t, tt.expected, refs)
		})
	}

	_, err := contentLabelsToReferences(map[string]string{}, dgst)
	require.EqualError(t, err, "no distribution source labels found for foo")
}
