package state

import (
	"context"
	"fmt"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/go-logr/logr"

	"github.com/xenitab/spegel/internal/store"
)

type EventTopic string

const (
	EventTopicCreate = "/images/create"
	EventTopicUpdate = "/images/update"
	EventTopicDelete = "/images/delete"
)

// TODO: explore issues when image is removed with a shared layer
func Track(ctx context.Context, containerdClient *containerd.Client, store store.Store) error {
	log := logr.FromContextOrDiscard(ctx)

	// Subscribe to image events before doing the initial sync to catch any changes which may occur inbetween.
	envelopeCh, errCh := containerdClient.EventService().Subscribe(ctx, `topic~="/images/*"`)
	imageCache, err := all(ctx, containerdClient, store)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
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

func all(ctx context.Context, containerdClient *containerd.Client, store store.Store) (map[string][]string, error) {
	imageCache := map[string][]string{}
	imgs, err := containerdClient.ListImages(ctx)
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

// TODO: Skip image if it has tag and digest and image already exists.
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
	ref, err := reference.Parse(img.Name())
	if err != nil {
		return nil, nil
		//return nil, err
	}
	layers := []string{}

	// Add image name and digest
	layers = append(layers, ref.String())
	layers = append(layers, img.Target().Digest.String())

	// Add rootfs digests
	dgsts, err := img.RootFS(ctx)
	if err != nil {
		return nil, err
	}
	for _, dgst := range dgsts {
		layers = append(layers, dgst.String())
	}

	// Add manifest layers and config digest
	manifest, err := images.Manifest(ctx, img.ContentStore(), img.Target(), img.Platform())
	if err != nil {
		return nil, err
	}
	layers = append(layers, manifest.Config.Digest.String())
	for _, layer := range manifest.Layers {
		layers = append(layers, layer.Digest.String())
	}
	return layers, nil
}
