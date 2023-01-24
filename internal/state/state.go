package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/multierr"

	"github.com/xenitab/spegel/internal/store"
)

type EventTopic string

const (
	EventTopicCreate = "/images/create"
	EventTopicUpdate = "/images/update"
	EventTopicDelete = "/images/delete"
)

// TODO: issues will most likely occur when removing and image that shares layers with another
func Track(ctx context.Context, containerdClient *containerd.Client, store store.Store, imageFilter string) error {
	log := logr.FromContextOrDiscard(ctx)

	// Subscribe to image events before doing the initial sync to catch any changes which may occur inbetween.
	eventFilters := []string{`topic~="/images/*"`}
	if imageFilter != "" {
		eventFilters = append(eventFilters, fmt.Sprintf(`event.name~="%s"`, imageFilter))
	}
	envelopeCh, errCh := containerdClient.EventService().Subscribe(ctx, eventFilters...)
	imageCache, err := all(ctx, containerdClient, store, imageFilter)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			// Clean up all layers written to the store before exiting.
			errs := []error{}
			for k, v := range imageCache {
				log.Info("cleaning up store image layers", "image", k)
				err := store.Remove(ctx, v)
				if err != nil {
					errs = append(errs, err)
				}
			}
			return multierr.Combine(errs...)
		case e := <-envelopeCh:
			switch e.Topic {
			case EventTopicCreate:
				image := events.ImageCreate{}
				err := image.Unmarshal(e.Event.Value)
				if err != nil {
					return err
				}
				err = update(ctx, containerdClient, store, image.Name)
				if err != nil {
					return err
				}
			case EventTopicUpdate:
				image := events.ImageUpdate{}
				err := image.Unmarshal(e.Event.Value)
				if err != nil {
					return err
				}
				err = update(ctx, containerdClient, store, image.Name)
				if err != nil {
					return err
				}
			case EventTopicDelete:
				image := events.ImageDelete{}
				err := image.Unmarshal(e.Event.Value)
				if err != nil {
					return err
				}
				layers, ok := imageCache[image.Name]
				if !ok {
					log.Error(fmt.Errorf("%s not found", image.Name), "failed removing image layers")
					continue
				}
				err = store.Remove(ctx, layers)
				if err != nil {
					return err
				}
				delete(imageCache, image.Name)
			}
		case err := <-errCh:
			return err
		}
	}
}

func all(ctx context.Context, containerdClient *containerd.Client, store store.Store, imageFilter string) (map[string][]string, error) {
	imageFilters := []string{}
	if imageFilter != "" {
		imageFilters = append(imageFilters, fmt.Sprintf(`name~=%s`, imageFilter))
	}
	imageCache := map[string][]string{}
	imgs, err := containerdClient.ListImages(ctx, imageFilters...)
	if err != nil {
		return nil, err
	}
	for _, img := range imgs {
		layers, err := imageLayers(ctx, containerdClient, img)
		if err != nil {
			return nil, err
		}
		err = store.Add(ctx, layers)
		if err != nil {
			return nil, err
		}
		imageCache[img.Name()] = layers
	}
	return imageCache, nil
}

func update(ctx context.Context, containerdClient *containerd.Client, store store.Store, name string) error {
	img, err := containerdClient.GetImage(ctx, name)
	if err != nil {
		return err
	}
	layers, err := imageLayers(ctx, containerdClient, img)
	if err != nil {
		return err
	}
	err = store.Add(ctx, layers)
	if err != nil {
		return err
	}
	return nil
}

func imageLayers(ctx context.Context, containerdClient *containerd.Client, img containerd.Image) ([]string, error) {
	layers := []string{}

	name := getNameWithTag(ctx, img)
	if name != "" {
		layers = append(layers, name)
	}
	layers = append(layers, img.Target().Digest.String())

	// Add image config digest and image layers
	manifest, err := images.Manifest(ctx, img.ContentStore(), img.Target(), img.Platform())
	if err != nil {
		return nil, err
	}
	layers = append(layers, manifest.Config.Digest.String())
	for _, layer := range manifest.Layers {
		layers = append(layers, layer.Digest.String())
	}

	// If manifest is of list or index type it needs to be parsed separatly to add the manifest digest for the specific architecture.
	// This is because when the images manifest is fetched through containerd the plaform specific manifest is immediatly returned.
	if img.Metadata().Target.MediaType == images.MediaTypeDockerSchema2ManifestList || img.Metadata().Target.MediaType == ocispec.MediaTypeImageIndex {
		b, err := content.ReadBlob(ctx, containerdClient.ContentStore(), img.Target())
		if err != nil {
			return nil, err
		}
		var idx ocispec.Index
		if err := json.Unmarshal(b, &idx); err != nil {
			return nil, err
		}
		for _, manifest := range idx.Manifests {
			if !img.Platform().Match(*manifest.Platform) {
				continue
			}
			layers = append(layers, manifest.Digest.String())
			break
		}
	}

	return layers, nil
}

func getNameWithTag(ctx context.Context, img containerd.Image) string {
	// Layers will never be referenced by both tag and digest. The image name is only needed together with a tag.
	// The name will only be added with the tag if the image reference is a tag and digest or a tag.
	// It will be skipped all together when referencing with a digest as resolving the name is not needed.
	ref, err := reference.Parse(img.Name())
	// It is possible for an image to have an invalid name according to containerd reference spec.
	// This is ok all that this happens but it means the image name cannot be resolved by the mirror.
	if err != nil {
		logr.FromContextOrDiscard(ctx).Info("ignoring unparseable reference", "name", img.Name())
		return ""
	}
	tag, _, _ := strings.Cut(ref.Object, "@")
	if tag == "" {
		return ""
	}
	ref.Object = tag
	return ref.String()
}