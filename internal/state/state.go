package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/xenitab/spegel/internal/routing"
)

type EventTopic string

const (
	EventTopicCreate = "/images/create"
	EventTopicUpdate = "/images/update"
	EventTopicDelete = "/images/delete"
)

var advertisedImages = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "spegel_advertised_images",
	Help: "Number of images advertised to be availible.",
})

var advertisedLayers = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "spegel_advertised_layers",
	Help: "Number of layers advertised to be availible.",
})

func Track(ctx context.Context, containerdClient *containerd.Client, router routing.Router, imageFilter string) error {
	log := logr.FromContextOrDiscard(ctx)

	// Subscribe to image events before doing the initial sync to catch any changes which may occur inbetween.
	eventFilters := []string{`topic~="/images/*"`}
	if imageFilter != "" {
		eventFilters = append(eventFilters, fmt.Sprintf(`event.name~="%s"`, imageFilter))
	}
	envelopeCh, errCh := containerdClient.EventService().Subscribe(ctx, eventFilters...)

	// Setup expiration ticker to update key expiration before they expire
	immediate := make(chan time.Time, 1)
	immediate <- time.Now()
	expirationTicker := time.NewTicker(routing.KeyTTL - time.Minute)
	defer expirationTicker.Stop()
	ticker := merge(immediate, expirationTicker.C)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker:
			log.Info("updating layer expiration")
			err := all(ctx, containerdClient, router, imageFilter)
			if err != nil {
				return fmt.Errorf("failed to update layer expiration: %w", err)
			}
		case e := <-envelopeCh:
			switch e.Topic {
			case EventTopicCreate:
				image := events.ImageCreate{}
				err := image.Unmarshal(e.Event.Value)
				if err != nil {
					return err
				}
				err = update(ctx, containerdClient, router, image.Name)
				if err != nil {
					return err
				}
			case EventTopicUpdate:
				image := events.ImageUpdate{}
				err := image.Unmarshal(e.Event.Value)
				if err != nil {
					return err
				}
				err = update(ctx, containerdClient, router, image.Name)
				if err != nil {
					return err
				}
			}
		case err := <-errCh:
			return err
		}
	}
}

func all(ctx context.Context, containerdClient *containerd.Client, router routing.Router, imageFilter string) error {
	imageFilters := []string{}
	if imageFilter != "" {
		imageFilters = append(imageFilters, fmt.Sprintf(`name~=%s`, imageFilter))
	}
	imgs, err := containerdClient.ListImages(ctx, imageFilters...)
	if err != nil {
		return err
	}
	layerCount := 0
	for _, img := range imgs {
		layers, err := imageLayers(ctx, containerdClient, img)
		if err != nil {
			return err
		}
		err = router.Advertise(ctx, layers)
		if err != nil {
			return err
		}
		layerCount = layerCount + len(layers)
	}
	advertisedImages.Set(float64(len(imgs)))
	advertisedLayers.Set(float64(layerCount))
	return nil
}

func update(ctx context.Context, containerdClient *containerd.Client, router routing.Router, name string) error {
	img, err := containerdClient.GetImage(ctx, name)
	if err != nil {
		return err
	}
	layers, err := imageLayers(ctx, containerdClient, img)
	if err != nil {
		return err
	}
	err = router.Advertise(ctx, layers)
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

func merge(cs ...<-chan time.Time) <-chan time.Time {
	var wg sync.WaitGroup
	out := make(chan time.Time)

	output := func(c <-chan time.Time) {
		for n := range c {
			out <- n
		}
		wg.Done()
	}
	wg.Add(len(cs))
	for _, c := range cs {
		go output(c)
	}

	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
