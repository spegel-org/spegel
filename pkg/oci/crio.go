package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/containers/storage"
	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	// eventChannelBuffer is the buffer size for the OCI event channel.
	eventChannelBuffer = 100
	// imagePollInterval is how often to poll for new images in containers/storage.
	imagePollInterval = 2 * time.Second
	// debounceDelay is how long to wait after a metadata change before processing.
	debounceDelay = 100 * time.Millisecond
)

var _ Store = &CRIoClient{}

// CRIoClient accesses CRI-O's image content cache and containers/storage for P2P distribution.
// It serves both layer blobs (from image content cache) and manifests/configs (from containers/storage).
type CRIoClient struct {
	imageContentCacheDir string
	store                storage.Store
	mediaTypeIdx         *lru.Cache[digest.Digest, string]
}

// NewCRIoClient creates a new CRI-O client.
// imageContentCacheDir must be provided - it's the path to CRI-O's image content cache.
func NewCRIoClient(imageContentCacheDir string) (*CRIoClient, error) {
	mediaTypeIdx, err := lru.New[digest.Digest, string](100)
	if err != nil {
		return nil, fmt.Errorf("failed to create media type cache: %w", err)
	}

	// containers/storage is optional - it may not be accessible in nested container environments.
	var store storage.Store
	storeOpts, err := storage.DefaultStoreOptions()
	if err == nil {
		store, err = storage.GetStore(storeOpts)
		if err != nil {
			// Log but don't fail - storage access is optional.
			store = nil
		}
	}

	return &CRIoClient{
		imageContentCacheDir: imageContentCacheDir,
		store:                store,
		mediaTypeIdx:         mediaTypeIdx,
	}, nil
}

func (c *CRIoClient) Name() string {
	return "crio"
}

// blobPath returns the filesystem path for a blob in the image content cache.
func (c *CRIoClient) blobPath(dgst digest.Digest) string {
	return filepath.Join(c.imageContentCacheDir, "blobs", dgst.Algorithm().String(), dgst.Encoded())
}

func (c *CRIoClient) Close() error {
	if c.store != nil {
		_, err := c.store.Shutdown(false)
		return err
	}
	return nil
}

// ListImages returns all images from containers/storage.
func (c *CRIoClient) ListImages(ctx context.Context) ([]Image, error) {
	if c.store == nil {
		return nil, nil
	}
	images, err := c.store.Images()
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	result := []Image{}
	for _, img := range images {
		for _, name := range img.Names {
			// Skip names that don't look like registry references.
			if !strings.Contains(name, "/") {
				continue
			}
			parsed, err := ParseImage(name, WithDigest(img.Digest))
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "failed to parse image name", "name", name)
				continue
			}
			result = append(result, parsed)
		}
	}
	return result, nil
}

// ListContent returns all content (blobs and manifests) with their source references.
func (c *CRIoClient) ListContent(ctx context.Context) ([][]Reference, error) {
	contents := [][]Reference{}

	// First, collect all blobs from the blob cache metadata.
	cacheMetadata, err := c.loadCacheMetadata()
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "failed to load cache metadata")
	} else {
		for digestStr, blobMeta := range cacheMetadata.Blobs {
			dgst, err := digest.Parse(digestStr)
			if err != nil {
				continue
			}
			// Verify blob file exists.
			if _, err := os.Stat(c.blobPath(dgst)); os.IsNotExist(err) {
				continue
			}
			refs := []Reference{}
			for _, source := range blobMeta.Sources {
				if source.Registry == "" || source.Repository == "" {
					continue
				}
				refs = append(refs, Reference{
					Registry:   source.Registry,
					Repository: source.Repository,
					Digest:     dgst,
				})
			}
			if len(refs) > 0 {
				contents = append(contents, refs)
			}
		}
	}

	// Also include manifests from containers/storage.
	if c.store == nil {
		return contents, nil
	}
	images, err := c.store.Images()
	if err != nil {
		return contents, nil
	}

	for _, img := range images {
		if img.Digest == "" {
			continue
		}
		for _, name := range img.Names {
			parsed, err := ParseImage(name)
			if err != nil {
				continue
			}
			contents = append(contents, []Reference{{
				Registry:   parsed.Registry,
				Repository: parsed.Repository,
				Digest:     img.Digest,
			}})
		}
	}

	return contents, nil
}

// Resolve returns the digest for a tagged image reference.
func (c *CRIoClient) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	if c.store == nil {
		return "", errors.Join(ErrNotFound, fmt.Errorf("storage not available"))
	}
	images, err := c.store.Images()
	if err != nil {
		return "", fmt.Errorf("failed to list images: %w", err)
	}

	for _, img := range images {
		for _, name := range img.Names {
			if name == ref {
				if img.Digest != "" {
					return img.Digest, nil
				}
				if len(img.Digests) > 0 {
					return img.Digests[0], nil
				}
				return "", fmt.Errorf("image %s has no digest", ref)
			}
		}
	}

	return "", errors.Join(ErrNotFound, fmt.Errorf("image not found: %s", ref))
}

// Descriptor returns the OCI descriptor for the given digest.
func (c *CRIoClient) Descriptor(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, error) {
	// First check blob cache.
	blobPath := c.blobPath(dgst)
	if info, err := os.Stat(blobPath); err == nil {
		mt, ok := c.mediaTypeIdx.Get(dgst)
		if !ok {
			mt = c.fingerprintMediaType(ctx, dgst, info.Size())
			c.mediaTypeIdx.Add(dgst, mt)
		}
		return ocispec.Descriptor{
			Size:      info.Size(),
			Digest:    dgst,
			MediaType: mt,
		}, nil
	}

	// Check containers/storage for manifests/configs.
	data, err := c.findInStorage(dgst)
	if err != nil {
		return ocispec.Descriptor{}, errors.Join(ErrNotFound, fmt.Errorf("blob %s not found", dgst))
	}

	mt, ok := c.mediaTypeIdx.Get(dgst)
	if !ok {
		mt, err = FingerprintMediaType(bytes.NewReader(data))
		if err != nil {
			mt = httpx.ContentTypeBinary
		}
		c.mediaTypeIdx.Add(dgst, mt)
	}
	return ocispec.Descriptor{
		Size:      int64(len(data)),
		Digest:    dgst,
		MediaType: mt,
	}, nil
}

// Open returns a reader for the blob content.
func (c *CRIoClient) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	// First check blob cache.
	blobPath := c.blobPath(dgst)
	if file, err := os.Open(blobPath); err == nil {
		return file, nil
	}

	// Check containers/storage for manifests/configs.
	data, err := c.findInStorage(dgst)
	if err != nil {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("blob %s not found", dgst))
	}

	return struct {
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: io.NewSectionReader(bytes.NewReader(data), 0, int64(len(data))),
		Closer:     io.NopCloser(nil),
	}, nil
}

// Subscribe returns a channel for OCI events.
// For CRI-O, we watch the metadata.json file for changes and emit events when new blobs are cached.
// We also poll containers/storage for new images to emit tag events.
func (c *CRIoClient) Subscribe(ctx context.Context) (<-chan OCIEvent, error) {
	log := logr.FromContextOrDiscard(ctx)
	ch := make(chan OCIEvent, eventChannelBuffer)

	// Create file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error(err, "failed to create file watcher, falling back to polling")
		return c.subscribePoll(ctx)
	}

	// Watch the blob cache directory for metadata.json changes.
	if err := watcher.Add(c.imageContentCacheDir); err != nil {
		log.Error(err, "failed to watch blob cache directory, falling back to polling")
		watcher.Close()
		return c.subscribePoll(ctx)
	}

	knownBlobs, knownImages := c.initKnownState()
	var mu sync.Mutex

	go func() {
		defer close(ch)
		defer watcher.Close()

		var debounceTimer *time.Timer
		var debounceCh <-chan time.Time

		imageTicker := time.NewTicker(imagePollInterval)
		defer imageTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return
			case <-imageTicker.C:
				mu.Lock()
				c.checkNewImages(ctx, ch, knownImages, log)
				mu.Unlock()
			case <-debounceCh:
				mu.Lock()
				c.checkNewBlobs(ctx, ch, knownBlobs, log)
				c.checkNewImages(ctx, ch, knownImages, log)
				mu.Unlock()
				debounceTimer = nil
				debounceCh = nil
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != "metadata.json" {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				// Reset debounce timer on each event.
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.NewTimer(debounceDelay)
				debounceCh = debounceTimer.C
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error(err, "file watcher error")
			}
		}
	}()

	return ch, nil
}

// initKnownState initializes the tracking maps for known blobs and images.
func (c *CRIoClient) initKnownState() (knownBlobs, knownImages map[string]bool) {
	knownBlobs = make(map[string]bool)
	if metadata, err := c.loadCacheMetadata(); err == nil {
		for digestStr := range metadata.Blobs {
			knownBlobs[digestStr] = true
		}
	}

	knownImages = make(map[string]bool)
	if c.store != nil {
		if images, err := c.store.Images(); err == nil {
			for _, img := range images {
				knownImages[img.ID] = true
			}
		}
	}
	return
}

// checkNewBlobs checks for new blobs in the cache metadata and emits events.
func (c *CRIoClient) checkNewBlobs(ctx context.Context, ch chan<- OCIEvent, knownBlobs map[string]bool, log logr.Logger) {
	metadata, err := c.loadCacheMetadata()
	if err != nil {
		log.Error(err, "failed to load cache metadata")
		return
	}

	for digestStr, blobMeta := range metadata.Blobs {
		if knownBlobs[digestStr] {
			continue
		}
		knownBlobs[digestStr] = true

		dgst, err := digest.Parse(digestStr)
		if err != nil {
			continue
		}

		for _, source := range blobMeta.Sources {
			if source.Registry == "" || source.Repository == "" {
				continue
			}
			log.V(1).Info("detected new blob", "digest", digestStr, "registry", source.Registry, "repository", source.Repository)
			if !sendEvent(ctx, ch, OCIEvent{
				Type: CreateEvent,
				Reference: Reference{
					Registry:   source.Registry,
					Repository: source.Repository,
					Digest:     dgst,
				},
			}) {
				return
			}
		}
	}
}

// sendEvent sends an event to the channel, returning false if context is done.
func sendEvent(ctx context.Context, ch chan<- OCIEvent, event OCIEvent) bool {
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// checkNewImages checks containers/storage for new images and emits tag, manifest, and config digest events.
func (c *CRIoClient) checkNewImages(ctx context.Context, ch chan<- OCIEvent, knownImages map[string]bool, log logr.Logger) {
	if c.store == nil {
		return
	}

	images, err := c.store.Images()
	if err != nil {
		return
	}

	for _, img := range images {
		if knownImages[img.ID] {
			continue
		}
		knownImages[img.ID] = true

		// Collect all BigData digests (configs, manifests stored in storage).
		bigDataDigests := c.collectBigDataDigests(img.ID)

		for _, name := range img.Names {
			if !strings.Contains(name, "/") {
				continue
			}
			parsed, err := ParseImage(name, WithDigest(img.Digest))
			if err != nil {
				continue
			}
			tagName, ok := parsed.TagName()
			if !ok {
				continue
			}
			log.Info("detected new image", "name", name, "tag", tagName, "digest", img.Digest)

			// Emit tag event.
			if !sendEvent(ctx, ch, OCIEvent{
				Type: CreateEvent,
				Reference: Reference{
					Registry:   parsed.Registry,
					Repository: parsed.Repository,
					Tag:        parsed.Tag,
				},
			}) {
				return
			}

			// Emit events for all manifest digests (manifest list + platform-specific).
			for _, dgst := range img.Digests {
				if dgst == "" {
					continue
				}
				if !sendEvent(ctx, ch, OCIEvent{
					Type: CreateEvent,
					Reference: Reference{
						Registry:   parsed.Registry,
						Repository: parsed.Repository,
						Digest:     dgst,
					},
				}) {
					return
				}
			}

			// Emit events for BigData digests (configs, manifests in storage).
			for _, dgst := range bigDataDigests {
				if !sendEvent(ctx, ch, OCIEvent{
					Type: CreateEvent,
					Reference: Reference{
						Registry:   parsed.Registry,
						Repository: parsed.Repository,
						Digest:     dgst,
					},
				}) {
					return
				}
			}
		}
	}
}

// collectBigDataDigests returns digests of all BigData items for an image.
func (c *CRIoClient) collectBigDataDigests(imageID string) []digest.Digest {
	var digests []digest.Digest
	names, err := c.store.ListImageBigData(imageID)
	if err != nil {
		return digests
	}
	for _, name := range names {
		if data, err := c.store.ImageBigData(imageID, name); err == nil {
			digests = append(digests, digest.FromBytes(data))
		}
	}
	return digests
}

// subscribePoll is a fallback that polls for metadata and image changes.
func (c *CRIoClient) subscribePoll(ctx context.Context) (<-chan OCIEvent, error) {
	log := logr.FromContextOrDiscard(ctx)
	ch := make(chan OCIEvent, eventChannelBuffer)
	knownBlobs, knownImages := c.initKnownState()
	var mu sync.Mutex

	go func() {
		defer close(ch)
		ticker := time.NewTicker(imagePollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				c.checkNewBlobs(ctx, ch, knownBlobs, log)
				c.checkNewImages(ctx, ch, knownImages, log)
				mu.Unlock()
			}
		}
	}()

	return ch, nil
}

// Verify checks if the CRI-O image content cache is accessible.
func (c *CRIoClient) Verify(ctx context.Context, _ string) error {
	// Verify image content cache directory exists.
	blobsDir := filepath.Join(c.imageContentCacheDir, "blobs")
	info, err := os.Stat(blobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("CRI-O image content cache directory does not exist: %s", blobsDir)
		}
		return fmt.Errorf("failed to access image content cache directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("image content cache path is not a directory: %s", blobsDir)
	}
	return nil
}

// findInStorage searches for content in containers/storage BigData.
func (c *CRIoClient) findInStorage(dgst digest.Digest) ([]byte, error) {
	if c.store == nil {
		return nil, fmt.Errorf("storage not available")
	}
	images, err := c.store.Images()
	if err != nil {
		return nil, err
	}

	for _, img := range images {
		names, err := c.store.ListImageBigData(img.ID)
		if err != nil {
			continue
		}

		for _, name := range names {
			data, err := c.store.ImageBigData(img.ID, name)
			if err != nil {
				continue
			}
			if digest.FromBytes(data) == dgst {
				return data, nil
			}
		}
	}

	return nil, fmt.Errorf("content not found in storage")
}

// fingerprintMediaType determines the media type of a blob.
func (c *CRIoClient) fingerprintMediaType(ctx context.Context, dgst digest.Digest, size int64) string {
	if size > ManifestMaxSize {
		return httpx.ContentTypeBinary
	}

	rc, err := c.Open(ctx, dgst)
	if err != nil {
		return httpx.ContentTypeBinary
	}
	defer rc.Close()

	mt, err := FingerprintMediaType(rc)
	if err != nil {
		return httpx.ContentTypeBinary
	}
	return mt
}

// CRIOCacheMetadata represents the blob cache metadata.json structure.
type CRIOCacheMetadata struct {
	Blobs map[string]CRIOBlobMetadata `json:"blobs"`
}

// CRIOBlobMetadata represents metadata for a single blob.
type CRIOBlobMetadata struct {
	Digest  string           `json:"digest"`
	Size    int64            `json:"size"`
	Sources []CRIOBlobSource `json:"sources"`
}

// CRIOBlobSource represents a source for a blob.
type CRIOBlobSource struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
}

// loadCacheMetadata loads the blob cache metadata.
func (c *CRIoClient) loadCacheMetadata() (*CRIOCacheMetadata, error) {
	metadataPath := filepath.Join(c.imageContentCacheDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if os.IsNotExist(err) {
		return &CRIOCacheMetadata{Blobs: make(map[string]CRIOBlobMetadata)}, nil
	}
	if err != nil {
		return nil, err
	}

	var metadata CRIOCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	if metadata.Blobs == nil {
		metadata.Blobs = make(map[string]CRIOBlobMetadata)
	}
	return &metadata, nil
}
