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
	"strings"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/internal/resilient"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

var _ oci.Store = &Containerd{}
var _ oci.ImageLister = &Containerd{}
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
	if err != nil {
		return "", err
	}
	return cImg.Target.Digest, nil
}

func (c *Containerd) Descriptor(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if errors.Is(err, errdefs.ErrNotFound) {
		return ocispec.Descriptor{}, errors.Join(oci.ErrNotFound, err)
	}
	if err != nil {
		return ocispec.Descriptor{}, err
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
			return ocispec.Descriptor{}, err
		}
		c.mediaTypeIdx.Add(dgst, mt)
	}

	desc := ocispec.Descriptor{
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
			return nil, errors.Join(oci.ErrNotFound, err)
		}
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, errors.Join(oci.ErrNotFound, err)
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
	eventFilters := []string{`topic~="/images/create|/images/delete",event.name~="^.+/"`, `topic~="/content/create"`}
	envelopeCh, cErrCh := c.client.EventService().Subscribe(subCtx, eventFilters...)

	// Populate the content index.
	contentIdx := map[digest.Digest][]oci.Reference{}
	initial := []store.Event{}

	imgs, err := c.ListImages(ctx)
	if err != nil {
		subCancel()
		return nil, nil, err
	}
	for _, img := range imgs {
		refs, err := walkImage(ctx, c.client, img)
		if err != nil {
			subCancel()
			return nil, nil, err
		}
		contentIdx[img.Digest] = refs
		for i, ref := range refs {
			event := store.Event{Type: store.CreateEvent, Digest: ref.Digest}
			if tagName, ok := img.TagName(); ok && i == 0 {
				event.Reference = tagName
			}
			initial = append(initial, event)
		}
	}

	go func() {
		defer close(eventCh)
		for {
			select {
			case <-subCtx.Done():
				return
			case envelope := <-envelopeCh:
				events, err := c.handleEvent(subCtx, *envelope, contentIdx)
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

func (c *Containerd) handleEvent(ctx context.Context, envelope events.Envelope, contentIdx map[digest.Digest][]oci.Reference) ([]store.Event, error) {
	if envelope.Event == nil {
		return nil, errors.New("envelope event cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope event: %w", err)
	}

	switch e := evt.(type) {
	case *eventtypes.ContentCreate:
		dgst := digest.Digest(e.GetDigest())
		refs, err := resilient.RetryValue(ctx, 10, resilient.BackoffDelay(10*time.Millisecond, 100*time.Millisecond), func(ctx context.Context) ([]oci.Reference, error) {
			info, err := c.client.ContentStore().Info(ctx, dgst)
			if err != nil {
				return nil, resilient.Unrecoverable(err)
			}
			refs, err := contentLabelsToReferences(info.Labels, dgst)
			if err != nil {
				return nil, err
			}
			return refs, nil
		})
		if err != nil {
			return nil, err
		}
		events := []store.Event{}
		for _, ref := range refs {
			events = append(events, store.Event{Type: store.CreateEvent, Digest: ref.Digest})
		}
		return events, nil
	case *eventtypes.ImageCreate:
		img, err := oci.ParseImage(e.GetName(), oci.AllowTagOnly())
		if err != nil {
			return nil, err
		}
		if oci.MatchesFilter(img.Reference, c.filters) {
			return nil, nil
		}
		// Just advertise the image if it is a tag reference.
		if tagName, ok := img.TagName(); ok {
			return []store.Event{{Type: store.CreateEvent, Reference: tagName}}, nil
		}
		// Walk the image to index its content.
		refs, err := walkImage(ctx, c.client, img)
		if err != nil {
			return nil, err
		}
		contentIdx[img.Digest] = refs
		return nil, nil
	case *eventtypes.ImageDelete:
		img, err := oci.ParseImage(e.GetName(), oci.AllowTagOnly())
		if err != nil {
			return nil, err
		}
		if oci.MatchesFilter(img.Reference, c.filters) {
			return nil, nil
		}
		// Just advertise the image if it is a tag reference.
		if tagName, ok := img.TagName(); ok {
			return []store.Event{{Type: store.DeleteEvent, Reference: tagName}}, nil
		}
		// Advertise deletion of images content if it no longer exists.
		refs, ok := contentIdx[img.Digest]
		if !ok {
			logr.FromContextOrDiscard(ctx).Info("delete event with missing content index entry")
			return []store.Event{{Type: store.DeleteEvent, Digest: img.Digest}}, nil
		}
		delete(contentIdx, img.Digest)
		// Delete events are sent before garbage collection is run.
		err = resilient.Retry(ctx, 10, resilient.BackoffDelay(10*time.Millisecond, 100*time.Millisecond), func(ctx context.Context) error {
			_, err := c.client.ContentStore().Info(ctx, img.Digest)
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil
			}
			if err != nil {
				return resilient.Unrecoverable(err)
			}
			return fmt.Errorf("manifest with digest %s still exists", img.Digest.String())
		}, resilient.WithLastErrorOnly())
		if err != nil {
			return nil, fmt.Errorf("image manifest has not been deleted: %w", err)
		}
		// Create delete events for contents that has been removed.
		events := []store.Event{}
		for _, ref := range refs {
			_, err := c.client.ContentStore().Info(ctx, ref.Digest)
			if err == nil {
				continue
			}
			if !errors.Is(err, errdefs.ErrNotFound) {
				return nil, err
			}
			events = append(events, store.Event{Type: store.DeleteEvent, Digest: ref.Digest})
		}
		return events, nil
	default:
		return nil, errors.New("unsupported event type")
	}
}

func walkImage(ctx context.Context, client *client.Client, img oci.Image) ([]oci.Reference, error) {
	cImgs, err := client.ImageService().List(ctx, fmt.Sprintf(`target.digest==%q`, img.Digest.String()))
	if err != nil {
		return nil, err
	}
	if len(cImgs) == 0 {
		return nil, fmt.Errorf("image %s not found", img.String())
	}
	refs := []oci.Reference{}
	handler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		_, err := client.ContentStore().Info(ctx, desc.Digest)
		if errors.Is(err, errdefs.ErrNotFound) {
			return nil, images.ErrSkipDesc
		}
		if err != nil {
			return nil, err
		}
		ref := oci.Reference{
			Registry:   img.Registry,
			Repository: img.Repository,
			Digest:     desc.Digest,
		}
		refs = append(refs, ref)
		return nil, nil
	})
	err = images.Walk(ctx, images.Handlers(handler, images.ChildrenHandler(client.ContentStore())), cImgs[0].Target)
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func contentLabelsToReferences(l map[string]string, dgst digest.Digest) ([]oci.Reference, error) {
	refs := []oci.Reference{}
	for k, v := range l {
		if !strings.HasPrefix(k, labels.LabelDistributionSource) {
			continue
		}
		ref := oci.Reference{
			Registry:   strings.TrimPrefix(k, labels.LabelDistributionSource+"."),
			Repository: v,
			Digest:     dgst,
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("no distribution source labels found for %s", dgst)
	}
	return refs, nil
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
