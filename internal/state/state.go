package state

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/xenitab/pkg/channels"

	"github.com/xenitab/spegel/internal/oci"
	"github.com/xenitab/spegel/internal/routing"
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
func Track(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
	log := logr.FromContextOrDiscard(ctx)
	eventCh, errCh := ociClient.Subscribe(ctx)
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
			err := all(ctx, ociClient, router, resolveLatestTag)
			if err != nil {
				return fmt.Errorf("failed to update all images: %w", err)
			}
		case img := <-eventCh:
			log.Info("received image event", "image", img)
			_, err := update(ctx, ociClient, router, img, false, resolveLatestTag)
			if err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}

func all(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
	imgs, err := ociClient.ListImages(ctx)
	if err != nil {
		return err
	}
	advertisedImages.Reset()
	advertisedKeys.Reset()
	targets := map[string]interface{}{}
	for _, img := range imgs {
		_, skipDigests := targets[img.Digest.String()]
		keyTotal, err := update(ctx, ociClient, router, img, skipDigests, resolveLatestTag)
		if err != nil {
			return err
		}
		targets[img.Digest.String()] = nil
		advertisedImages.WithLabelValues(img.Registry).Add(1)
		advertisedKeys.WithLabelValues(img.Registry).Add(float64(keyTotal))
	}
	return nil
}

func update(ctx context.Context, ociClient oci.Client, router routing.Router, img oci.Image, skipDigests, resolveLatestTag bool) (int, error) {
	keys := []string{}
	if !(!resolveLatestTag && img.IsLatestTag()) {
		if tagRef, ok := img.TagName(); ok {
			keys = append(keys, tagRef)
		}
	}
	if !skipDigests {
		dgsts, err := ociClient.GetImageDigests(ctx, img)
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
