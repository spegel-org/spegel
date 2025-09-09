package state

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/internal/channel"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func Track(ctx context.Context, ociStore oci.Store, router routing.Router, resolveLatestTag bool) error {
	log := logr.FromContextOrDiscard(ctx)
	eventCh, err := ociStore.Subscribe(ctx)
	if err != nil {
		return err
	}
	immediateCh := make(chan time.Time, 1)
	immediateCh <- time.Now()
	close(immediateCh)
	expirationTicker := time.NewTicker(routing.KeyTTL - time.Minute)
	defer expirationTicker.Stop()
	tickerCh := channel.Merge(immediateCh, expirationTicker.C)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tickerCh:
			log.Info("running state update")
			err := tick(ctx, ociStore, router, resolveLatestTag)
			if err != nil {
				log.Error(err, "received errors when updating all images")
				continue
			}
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("event channel closed")
			}
			log.Info("OCI event", "key", event.Key, "type", event.Type)
			err := handle(ctx, router, event)
			if err != nil {
				log.Error(err, "could not handle event")
				continue
			}
		}
	}
}

func tick(ctx context.Context, ociStore oci.Store, router routing.Router, resolveLatest bool) error {
	advertisedImages := map[string]float64{}
	advertisedImageDigests := map[string]float64{}
	advertisedImageTags := map[string]float64{}
	advertisedKeys := map[string]float64{}

	imgs, err := ociStore.ListImages(ctx)
	if err != nil {
		return err
	}
	for _, img := range imgs {
		advertisedImages[img.Registry] += 1
		advertisedImageDigests[img.Registry] += 1
		if !resolveLatest && img.IsLatestTag() {
			continue
		}
		tagName, ok := img.TagName()
		if !ok {
			continue
		}
		err := router.Advertise(ctx, []string{tagName}, false)
		if err != nil {
			return err
		}
		advertisedImageTags[img.Registry] += 1
		advertisedKeys[img.Registry] += 1
	}

	contents, err := ociStore.ListContents(ctx)
	if err != nil {
		return err
	}
	for _, content := range contents {
		err := router.Advertise(ctx, []string{content.Digest.String()}, false)
		if err != nil {
			return err
		}
		for _, registry := range content.Registires {
			advertisedKeys[registry] += 1
		}
	}

	for k, v := range advertisedImages {
		metrics.AdvertisedImages.WithLabelValues(k).Set(v)
	}
	for k, v := range advertisedImageDigests {
		metrics.AdvertisedImageDigests.WithLabelValues(k).Set(v)
	}
	for k, v := range advertisedImageTags {
		metrics.AdvertisedImageTags.WithLabelValues(k).Set(v)
	}
	for k, v := range advertisedKeys {
		metrics.AdvertisedKeys.WithLabelValues(k).Set(v)
	}
	return nil
}

func handle(ctx context.Context, router routing.Router, event oci.OCIEvent) error {
	if event.Type != oci.CreateEvent {
		return nil
	}
	err := router.Advertise(ctx, []string{event.Key}, false)
	if err != nil {
		return err
	}
	return nil
}
