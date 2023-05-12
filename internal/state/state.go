package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/events"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/reference"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/xenitab/spegel/internal/routing"
	"github.com/xenitab/spegel/internal/utils"
)

type EventTopic string

const (
	EventTopicCreate = "/images/create"
	EventTopicUpdate = "/images/update"
	EventTopicDelete = "/images/delete"
)

var advertisedImages = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "spegel_advertised_images",
	Help: "Number of images advertised to be availible.",
}, []string{"registry"})

var advertisedKeys = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "spegel_advertised_keys",
	Help: "Number of keys advertised to be availible.",
}, []string{"registry"})

// TODO: Update metrics on subscribed events. This will require keeping state in memory to know about key count changes.
func Track(ctx context.Context, containerdClient *containerd.Client, router routing.Router, registries []url.URL, imageFilter string) error {
	log := logr.FromContextOrDiscard(ctx)

	// Create filters
	listFilter, eventFilter := createFilters(registries, imageFilter)
	log.Info("tracking images with filters", "event", eventFilter, "list", listFilter)

	// Subscribe to image events before doing the initial sync to catch any changes which may occur inbetween.
	envelopeCh, errCh := containerdClient.EventService().Subscribe(ctx, eventFilter)

	// Setup expiration ticker to update key expiration before they expire
	immediate := make(chan time.Time, 1)
	immediate <- time.Now()
	expirationTicker := time.NewTicker(routing.KeyTTL - time.Minute)
	defer expirationTicker.Stop()
	ticker := utils.MergeChannels(immediate, expirationTicker.C)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker:
			log.Info("running scheduled image state update")
			err := all(ctx, containerdClient, router, listFilter)
			if err != nil {
				return fmt.Errorf("failed to update all images: %w", err)
			}
		case e := <-envelopeCh:
			name, err := getEventImageName(e)
			if err != nil {
				return err
			}
			img, err := containerdClient.GetImage(ctx, name)
			if err != nil {
				return err
			}
			_, _, err = update(ctx, containerdClient, router, img, false)
			if err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}

func all(ctx context.Context, containerdClient *containerd.Client, router routing.Router, filter string) error {
	imgs, err := containerdClient.ListImages(ctx, filter)
	if err != nil {
		return err
	}
	advertisedImages.Reset()
	advertisedKeys.Reset()
	targets := map[string]interface{}{}
	for _, img := range imgs {
		_, skipDigests := targets[img.Target().Digest.String()]
		registry, keyTotal, err := update(ctx, containerdClient, router, img, skipDigests)
		if err != nil {
			return err
		}
		targets[img.Target().Digest.String()] = nil
		advertisedImages.WithLabelValues(registry).Add(1)
		advertisedKeys.WithLabelValues(registry).Add(float64(keyTotal))
	}
	return nil
}

func update(ctx context.Context, containerdClient *containerd.Client, router routing.Router, img containerd.Image, skipDigests bool) (string, int, error) {
	// Parse image reference
	ref, err := reference.Parse(img.Name())
	if err != nil {
		return "", 0, err
	}

	// Images can be referenced with both tag and digest. The image name is however only needed when resolving a tag to a digest.
	// For this reason it is only of interest to advertise image names with only the tag.
	keys := []string{}
	tag, _, _ := strings.Cut(ref.Object, "@")
	if tag != "" {
		ref.Object = tag
		keys = append(keys, ref.String())
	}

	if !skipDigests {
		dgsts, err := getAllImageDigests(ctx, containerdClient, img)
		if err != nil {
			return "", 0, err
		}
		keys = append(keys, dgsts...)
	}

	err = router.Advertise(ctx, keys)
	if err != nil {
		return "", 0, err
	}

	return ref.Hostname(), len(keys), nil
}

func getAllImageDigests(ctx context.Context, containerdClient *containerd.Client, img containerd.Image) ([]string, error) {
	manifest, err := images.Manifest(ctx, img.ContentStore(), img.Target(), img.Platform())
	if err != nil {
		return nil, err
	}

	// Add image digest, config and image layers
	keys := []string{}
	keys = append(keys, img.Target().Digest.String())
	keys = append(keys, manifest.Config.Digest.String())
	for _, layer := range manifest.Layers {
		keys = append(keys, layer.Digest.String())
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
			keys = append(keys, manifest.Digest.String())
			break
		}
	}

	return keys, nil
}

func getEventImageName(e *events.Envelope) (string, error) {
	switch e.Topic {
	case EventTopicCreate, EventTopicUpdate:
		imageName, ok := e.Field([]string{"event.name"})
		if !ok {
			return "", fmt.Errorf("field event.name not found")
		}
		return imageName, nil
	default:
		return "", fmt.Errorf("unknown topic: %s", e.Topic)
	}
}

func createFilters(registries []url.URL, imageFilter string) (string, string) {
	registryHosts := []string{}
	for _, registry := range registries {
		registryHosts = append(registryHosts, registry.Host)
	}
	if imageFilter != "" {
		imageFilter = "|" + imageFilter
	}
	listFilter := fmt.Sprintf(`name~="%s%s"`, strings.Join(registryHosts, "|"), imageFilter)
	eventFilter := fmt.Sprintf(`topic~="/images/create|/images/update",event.name~="%s%s"`, strings.Join(registryHosts, "|"), imageFilter)
	return listFilter, eventFilter
}
