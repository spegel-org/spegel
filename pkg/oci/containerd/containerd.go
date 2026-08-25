package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

var _ oci.ImageLister = &Containerd{}
var _ store.Provider = &Containerd{}
var _ store.Watcher = &Containerd{}

type ContainerdConfig struct {
	Conn        net.Conn
	ContentPath string
	Filters     []oci.Filter
}

type ContainerdOption = option.Option[ContainerdConfig]

func WithContentPath(path string) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.ContentPath = path
		return nil
	}
}

func WithConnection(conn net.Conn) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.Conn = conn
		return nil
	}
}

func WithFilters(filters []oci.Filter) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.Filters = filters
		return nil
	}
}

type Containerd struct {
	client       *client.Client
	mediaTypeIdx *lru.Cache[digest.Digest, string]
	contentPath  string
	filters      []oci.Filter
}

func NewContainerd(ctx context.Context, socketPath, namespace string, opts ...ContainerdOption) (*Containerd, error) {
	cfg := ContainerdConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}

	clientOpts := []client.Opt{
		client.WithDefaultNamespace(namespace),
	}
	if cfg.Conn != nil {
		dialOpt := grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return cfg.Conn, nil
		})
		clientOpts = append(clientOpts, client.WithExtraDialOpts([]grpc.DialOption{dialOpt}))
	}
	client, err := client.New(socketPath, clientOpts...)
	if err != nil {
		return nil, err
	}

	contentPath := cfg.ContentPath
	if contentPath == "" {
		contentPath, err = getContentPath(ctx, client)
		if err != nil {
			return nil, err
		}
	}

	mediaTypeIdx, err := lru.New[digest.Digest, string](100)
	if err != nil {
		return nil, err
	}

	c := &Containerd{
		client:       client,
		mediaTypeIdx: mediaTypeIdx,
		contentPath:  contentPath,
	}
	return c, nil
}

func (c *Containerd) Close() error {
	err := c.client.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *Containerd) Name() string {
	return "containerd"
}

func (c *Containerd) ListImages(ctx context.Context) ([]oci.Image, error) {
	cImgs, err := c.client.ImageService().List(ctx, `name~="^.+/"`)
	if err != nil {
		return nil, err
	}
	tagDgsts := map[digest.Digest]string{}
	imgs := []oci.Image{}
	for _, cImg := range cImgs {
		img, err := oci.ParseImage(cImg.Name, oci.WithDigest(cImg.Target.Digest))
		if err != nil {
			return nil, err
		}
		if img.Tag != "" {
			tagDgsts[img.Digest] = img.Tag
		}
		if oci.MatchesFilter(img.Reference, c.filters) {
			continue
		}
		imgs = append(imgs, img)
	}
	// Remove duplicate digest images that already have tags.
	imgs = slices.DeleteFunc(imgs, func(img oci.Image) bool {
		if img.Tag != "" {
			return false
		}
		if _, ok := tagDgsts[img.Digest]; ok {
			return true
		}
		return false
	})
	return imgs, nil
}

func (c *Containerd) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	cImg, err := c.client.ImageService().Get(ctx, ref)
	if errors.Is(err, errdefs.ErrNotFound) {
		return "", errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return "", err
	}
	return cImg.Target.Digest, nil
}

func (c *Containerd) Descriptor(ctx context.Context, dgst digest.Digest) (store.Descriptor, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if errors.Is(err, errdefs.ErrNotFound) {
		return store.Descriptor{}, errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return store.Descriptor{}, err
	}

	mt, ok := c.mediaTypeIdx.Get(dgst)
	if !ok {
		mt, err = func() (string, error) {
			if info.Size > oci.ManifestMaxSize {
				return httpx.ContentTypeBinary, nil
			}
			rc, err := c.Open(ctx, dgst)
			if err != nil {
				return "", err
			}
			defer rc.Close()
			mt, err := oci.FingerprintMediaType(rc)
			if err != nil {
				return "", err
			}
			return mt, nil
		}()
		if err != nil {
			return store.Descriptor{}, err
		}
		c.mediaTypeIdx.Add(dgst, mt)
	}

	desc := store.Descriptor{
		Size:      info.Size,
		Digest:    dgst,
		MediaType: mt,
	}
	return desc, nil
}

func (c *Containerd) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	if c.contentPath != "" {
		path := filepath.Join(c.contentPath, "blobs", dgst.Algorithm().String(), dgst.Encoded())
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(store.ErrNotFound, err)
		}
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return struct {
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: io.NewSectionReader(ra, 0, ra.Size()),
		Closer:     ra,
	}, nil
}

func (c *Containerd) Watch(ctx context.Context) ([]store.Event, <-chan store.Event, error) {
	log := logr.FromContextOrDiscard(ctx)

	eventCh := make(chan store.Event)
	subCtx, subCancel := context.WithCancel(ctx)
	eventFilters := []string{
		fmt.Sprintf(`namespace==%q,topic~="/images/create|/images/update",event.name~="^.+/"`, c.client.DefaultNamespace()),
		fmt.Sprintf(`namespace==%q,topic~="/images/delete"`, c.client.DefaultNamespace()),
		fmt.Sprintf(`namespace==%q,topic~="/content/create"`, c.client.DefaultNamespace()),
		fmt.Sprintf(`namespace==%q,topic~="/snapshot/remove"`, c.client.DefaultNamespace()),
	}
	envelopeCh, cErrCh := c.client.EventService().Subscribe(subCtx, eventFilters...)

	// Populate the content index.
	idx := NewIndex()
	initial := []store.Event{}

	imgs, err := c.ListImages(ctx)
	if err != nil {
		subCancel()
		return nil, nil, err
	}
	for _, img := range imgs {
		initial = append(initial, idx.AddImage(img)...)
	}
	err = c.client.ContentStore().Walk(ctx, func(info content.Info) error {
		initial = append(initial, idx.AddContent(info.Digest)...)
		return nil
	})
	if err != nil {
		subCancel()
		return nil, nil, err
	}

	go func() {
		defer close(eventCh)
		for {
			select {
			case <-subCtx.Done():
				return
			case envelope := <-envelopeCh:
				events, err := c.handleEvent(subCtx, *envelope, idx)
				if err != nil {
					log.Error(err, "error when handling containerd event")
					continue
				}
				for _, event := range events {
					eventCh <- event
				}
			}
		}
	}()

	go func() {
		// Required so that the event channel closes in case Containerd is restarted.
		defer subCancel()
		for err := range cErrCh {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Error(err, "received containerd event error")
		}
	}()

	return initial, eventCh, nil
}

func (c *Containerd) handleEvent(ctx context.Context, envelope events.Envelope, idx *Index) ([]store.Event, error) {
	if envelope.Event == nil {
		return nil, errors.New("envelope event cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope event: %w", err)
	}

	switch e := evt.(type) {
	case *eventtypes.ContentCreate:
		dgst, err := digest.Parse(e.GetDigest())
		if err != nil {
			return nil, err
		}
		return idx.AddContent(dgst), nil
	case *eventtypes.ImageCreate:
		cImg, err := c.client.ImageService().Get(ctx, e.GetName())
		if err != nil {
			return nil, err
		}
		if cImg.UpdatedAt.After(envelope.Timestamp) {
			logr.FromContextOrDiscard(ctx).Info("skipping image that was updated after create event")
			return nil, nil
		}
		img, err := oci.ParseImage(e.GetName(), oci.WithDigest(cImg.Target.Digest))
		if err != nil {
			return nil, err
		}
		return idx.AddImage(img), nil
	case *eventtypes.ImageUpdate:
		cImg, err := c.client.ImageService().Get(ctx, e.GetName())
		if err != nil {
			return nil, err
		}
		if cImg.UpdatedAt.After(envelope.Timestamp) {
			logr.FromContextOrDiscard(ctx).Info("skipping image that was updated after update event")
			return nil, nil
		}
		img, err := oci.ParseImage(e.GetName(), oci.WithDigest(cImg.Target.Digest))
		if err != nil {
			return nil, err
		}
		return idx.AddImage(img), nil
	case *eventtypes.ImageDelete:
		if _, err := digest.Parse(e.GetName()); err == nil {
			dgsts := []digest.Digest{}
			err = c.client.ContentStore().Walk(ctx, func(info content.Info) error {
				dgsts = append(dgsts, info.Digest)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return idx.DiffContent(dgsts), nil
		}
		img, err := oci.ParseImage(e.GetName(), oci.AllowTagOnly())
		if err != nil {
			return nil, err
		}
		return idx.RemoveImage(img), nil
	case *eventtypes.SnapshotRemove:
		dgsts := []digest.Digest{}
		err = c.client.ContentStore().Walk(ctx, func(info content.Info) error {
			dgsts = append(dgsts, info.Digest)
			return nil
		})
		if err != nil {
			return nil, err
		}
		return idx.DiffContent(dgsts), nil
	default:
		return nil, fmt.Errorf("unsupported event type %T", evt)
	}
}

func getContentPath(ctx context.Context, client *client.Client) (string, error) {
	pluginInfo, err := client.IntrospectionService().PluginInfo(ctx, string(plugins.ContentPlugin), "content", nil)
	if err != nil {
		return "", err
	}
	root, ok := pluginInfo.Plugin.Exports["root"]
	if !ok {
		logr.FromContextOrDiscard(ctx).Info("falling back to reading content from socket as content path could not be found in plugin")
		return "", nil
	}
	ok, err = dirExists(root)
	if err != nil && !errors.Is(err, os.ErrPermission) {
		return "", err
	}
	if !ok {
		logr.FromContextOrDiscard(ctx).Info("falling back to reading content from socket as content path directory does not exist")
		return "", nil
	}
	return root, nil
}
