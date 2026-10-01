package containerd

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
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

func TestWithFilters(t *testing.T) {
	t.Parallel()

	filters := []oci.Filter{
		oci.RegistryWhitelistFilter{Whitelist: []string{"example.com"}},
	}
	ctrd, err := NewContainerd(t.Context(), "test.sock", "", WithContentPath("foobar"), WithConnection(&net.UnixConn{}), WithFilters(filters))
	require.NoError(t, err)
	require.Len(t, ctrd.filters, 1)

	require.True(t, oci.MatchesFilter(oci.Reference{Registry: "docker.io", Repository: "library/nginx", Tag: "latest"}, ctrd.filters))
	require.False(t, oci.MatchesFilter(oci.Reference{Registry: "example.com", Repository: "library/nginx", Tag: "latest"}, ctrd.filters))
}

var _ images.Store = &fakeImageStore{}

// fakeImageStore is a minimal images.Store that only supports lookups.
type fakeImageStore struct {
	imgs map[string]images.Image
}

func (f *fakeImageStore) Get(ctx context.Context, name string) (images.Image, error) {
	img, ok := f.imgs[name]
	if !ok {
		return images.Image{}, errdefs.ErrNotFound
	}
	return img, nil
}

func (f *fakeImageStore) List(ctx context.Context, filters ...string) ([]images.Image, error) {
	return nil, nil
}

func (f *fakeImageStore) Create(ctx context.Context, image images.Image) (images.Image, error) {
	return images.Image{}, nil
}

func (f *fakeImageStore) Update(ctx context.Context, image images.Image, fieldpaths ...string) (images.Image, error) {
	return images.Image{}, nil
}

func (f *fakeImageStore) Delete(ctx context.Context, name string, opts ...images.DeleteOpt) error {
	return nil
}

func TestHandleEventFilters(t *testing.T) {
	t.Parallel()

	const (
		nginx = "example.com/library/nginx:latest"
		redis = "example.com/library/redis:latest"
		pause = "docker.io/library/pause:latest"
	)
	imgStore := &fakeImageStore{imgs: map[string]images.Image{}}
	for _, name := range []string{nginx, redis, pause} {
		imgStore.imgs[name] = images.Image{Name: name, Target: ocispec.Descriptor{Digest: digest.FromString(name)}}
	}
	cl, err := client.New("test.sock", client.WithDefaultNamespace("test"), client.WithServices(client.WithImageStore(imgStore)))
	require.NoError(t, err)
	ctrd := Containerd{
		client: cl,
		filters: []oci.Filter{
			oci.RegistryWhitelistFilter{Whitelist: []string{"example.com"}},
			oci.RegexFilter{Regex: regexp.MustCompile(`/library/nginx`)},
		},
	}

	contentDgst := digest.FromString("content")
	tests := []struct {
		name           string
		event          typeurl.Any
		expectedEvents []store.Event
	}{
		{
			name:           "content is advertised without being filtered",
			event:          mustMarshalAny(t, &eventtypes.ContentCreate{Digest: contentDgst.String()}),
			expectedEvents: []store.Event{{Type: store.CreateEvent, Digest: contentDgst}},
		},
		{
			name:           "create image in mirrored registry",
			event:          mustMarshalAny(t, &eventtypes.ImageCreate{Name: redis}),
			expectedEvents: []store.Event{{Type: store.CreateEvent, Reference: redis}},
		},
		{
			name:           "create image in registry that is not mirrored",
			event:          mustMarshalAny(t, &eventtypes.ImageCreate{Name: pause}),
			expectedEvents: nil,
		},
		{
			name:           "create image matching registry filter",
			event:          mustMarshalAny(t, &eventtypes.ImageCreate{Name: nginx}),
			expectedEvents: nil,
		},
		{
			name:           "update already indexed image",
			event:          mustMarshalAny(t, &eventtypes.ImageUpdate{Name: redis}),
			expectedEvents: nil,
		},
		{
			name:           "update filtered image",
			event:          mustMarshalAny(t, &eventtypes.ImageUpdate{Name: nginx}),
			expectedEvents: nil,
		},
		{
			name:           "delete indexed image",
			event:          mustMarshalAny(t, &eventtypes.ImageDelete{Name: redis}),
			expectedEvents: []store.Event{{Type: store.DeleteEvent, Reference: redis}},
		},
		{
			name:           "delete image that was never indexed",
			event:          mustMarshalAny(t, &eventtypes.ImageDelete{Name: pause}),
			expectedEvents: nil,
		},
	}
	// The index is shared between the events as it would be in Watch.
	idx := NewIndex()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeEvts, err := ctrd.handleEvent(t.Context(), events.Envelope{Event: tt.event}, idx)
			require.NoError(t, err)
			require.Equal(t, tt.expectedEvents, storeEvts)
		})
	}
}

func mustMarshalAny(t *testing.T, v any) typeurl.Any {
	t.Helper()

	any, err := typeurl.MarshalAny(v)
	require.NoError(t, err)
	return any
}
