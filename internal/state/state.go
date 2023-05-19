package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/containerd/containerd"
	apievents "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/events"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/xenitab/pkg/channels"

	"github.com/xenitab/spegel/internal/oci"
	"github.com/xenitab/spegel/internal/routing"
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
	ticker := channels.Merge(immediate, expirationTicker.C)

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
			cImg, err := containerdClient.GetImage(ctx, name)
			if err != nil {
				return err
			}
			img, err := oci.ParseWithDigest(cImg.Name(), cImg.Target().Digest)
			if err != nil {
				return err
			}
			_, err = update(ctx, containerdClient, router, img, false)
			if err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}

func all(ctx context.Context, containerdClient *containerd.Client, router routing.Router, filter string) error {
	cImgs, err := containerdClient.ListImages(ctx, filter)
	if err != nil {
		return err
	}
	advertisedImages.Reset()
	advertisedKeys.Reset()
	targets := map[string]interface{}{}
	for _, cImg := range cImgs {
		img, err := oci.ParseWithDigest(cImg.Name(), cImg.Target().Digest)
		if err != nil {
			return err
		}
		_, skipDigests := targets[img.Digest.String()]
		keyTotal, err := update(ctx, containerdClient, router, img, skipDigests)
		if err != nil {
			return err
		}
		targets[img.Digest.String()] = nil
		advertisedImages.WithLabelValues(img.Registry).Add(1)
		advertisedKeys.WithLabelValues(img.Registry).Add(float64(keyTotal))
	}
	return nil
}

func update(ctx context.Context, containerdClient *containerd.Client, router routing.Router, img oci.Image, skipDigests bool) (int, error) {
	keys := []string{}
	if tagRef, ok := img.TagReference(); ok {
		keys = append(keys, tagRef)
	}
	if !skipDigests {
		dgsts, err := getAllImageDigests(ctx, containerdClient, img)
		if err != nil {
			return 0, err
		}
		keys = append(keys, dgsts...)
	}
	err := router.Advertise(ctx, keys)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

type unknownDocument struct {
	MediaType string `json:"mediaType,omitempty"`
}

func getAllImageDigests(ctx context.Context, containerdClient *containerd.Client, img oci.Image) ([]string, error) {
	keys := []string{}
	platform := platforms.Default()
	err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		b, err := content.ReadBlob(ctx, containerdClient.ContentStore(), desc)
		if err != nil {
			return nil, err
		}
		var ud unknownDocument
		if err := json.Unmarshal(b, &ud); err != nil {
			return nil, err
		}

		switch ud.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			var idx ocispec.Index
			if err := json.Unmarshal(b, &idx); err != nil {
				return nil, err
			}
			for _, manifest := range idx.Manifests {
				if !platform.Match(*manifest.Platform) {
					continue
				}
				keys = append(keys, manifest.Digest.String())
				return []ocispec.Descriptor{manifest}, nil
			}
			return nil, fmt.Errorf("could not find platform architecture in manifest: %v", desc.Digest)
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			var manifest ocispec.Manifest
			if err := json.Unmarshal(b, &manifest); err != nil {
				return nil, err
			}
			keys = append(keys, manifest.Config.Digest.String())
			for _, layer := range manifest.Layers {
				keys = append(keys, layer.Digest.String())
			}
			// TODO: In the images.Manifest implementation there is a platform check that I do not understand
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected media type %v for digest: %v", ud.MediaType, desc.Digest)
	}), ocispec.Descriptor{Digest: img.Digest})
	if err != nil {
		return nil, fmt.Errorf("failed to walk image manifests: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no image digests found")
	}
	keys = append(keys, img.Digest.String())
	return keys, nil
}

func getEventImageName(e *events.Envelope) (string, error) {
	switch e.Topic {
	case EventTopicCreate:
		img := apievents.ImageCreate{}
		err := img.Unmarshal(e.Event.Value)
		if err != nil {
			return "", err
		}
		return img.Name, nil
	case EventTopicUpdate:
		img := apievents.ImageUpdate{}
		err := img.Unmarshal(e.Event.Value)
		if err != nil {
			return "", err
		}
		return img.Name, nil
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
