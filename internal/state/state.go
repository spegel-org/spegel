package state

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/reference"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/xenitab/spegel/internal/routing"
	"github.com/xenitab/spegel/internal/utils"
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
	// listFilter, eventFilter := createFilters(registries, imageFilter)
	// log.Info("tracking images with filters", "event", eventFilter, "list", listFilter)

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

func updateAll(ctx context.Context, containerdClient *containerd.Client, router routing.Router, filter string) error {
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

func updateImage(ctx context.Context, containerdClient *containerd.Client, router routing.Router, img containerd.Image, skipDigests bool) (string, int, error) {
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
