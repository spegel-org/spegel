package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/xenitab/pkg/channels"

	"github.com/xenitab/spegel/pkg/metrics"
	"github.com/xenitab/spegel/pkg/oci"
	"github.com/xenitab/spegel/pkg/routing"
)

// TODO: Update metrics on subscribed events. This will require keeping state in memory to know about key count changes.
func Track(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) {
	log := logr.FromContextOrDiscard(ctx)
	for {
		err := track(ctx, ociClient, router, resolveLatestTag)
		if err == nil || errors.Is(err, context.Canceled) {
			log.V(5).Info("image state tracker stopped")
			return
		}
		log.Error(err, "restarting image state tracker due to error")
	}
}

func track(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
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
			return ctx.Err()
		case <-ticker:
			log.Info("running scheduled image state update")
			if err := all(ctx, ociClient, router, resolveLatestTag); err != nil {
				return fmt.Errorf("received errors when updating all images: %w", err)
			}
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("image event channel closed")
			}
			log.Info("received image event", "image", event.Image, "type", event.Type)
			if _, err := update(ctx, ociClient, router, event, false, resolveLatestTag); err != nil {
				log.Error(err, "received error when updating image")
				continue
			}
		case err, ok := <-errCh:
			if !ok {
				return errors.New("image error channel closed")
			}
			log.Error(err, "event channel error")
			continue
		}
	}
}

func all(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
	log := logr.FromContextOrDiscard(ctx).V(5)
	imgs, err := ociClient.ListImages(ctx)
	if err != nil {
		return err
	}

	metrics.AdvertisedKeys.Reset()
	metrics.AdvertisedImages.Reset()
	metrics.AdvertisedImageTags.Reset()
	metrics.AdvertisedImageDigests.Reset()
	errs := []error{}
	targets := map[string]interface{}{}
	for _, img := range imgs {
		_, skipDigests := targets[img.Digest.String()]
		// Handle the list re-sync as update events; this will also prevent the
		// update function from setting metrics values.
		event := oci.ImageEvent{Image: img, Type: oci.UpdateEvent}
		log.Info("sync image event", "image", event.Image, "type", event.Type)
		keyTotal, err := update(ctx, ociClient, router, event, skipDigests, resolveLatestTag)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		targets[img.Digest.String()] = nil
		metrics.AdvertisedKeys.WithLabelValues(img.Registry).Add(float64(keyTotal))
		metrics.AdvertisedImages.WithLabelValues(img.Registry).Add(1)
		if img.Tag == "" {
			metrics.AdvertisedImageDigests.WithLabelValues(event.Image.Registry).Add(1)
		} else {
			metrics.AdvertisedImageTags.WithLabelValues(event.Image.Registry).Add(1)
		}
	}
	return errors.Join(errs...)
}

func update(ctx context.Context, ociClient oci.Client, router routing.Router, event oci.ImageEvent, skipDigests, resolveLatestTag bool) (int, error) {
	keys := []string{}
	if !(!resolveLatestTag && event.Image.IsLatestTag()) {
		if tagRef, ok := event.Image.TagName(); ok {
			keys = append(keys, tagRef)
		}
	}
	if event.Type == oci.DeleteEvent {
		// We don't know how many digest keys were associated with the deleted image;
		// that can only be updated by the full image list sync in all().
		metrics.AdvertisedImages.WithLabelValues(event.Image.Registry).Sub(1)
		// DHT doesn't actually have any way to stop providing a key, you just have to wait for the record to expire
		// from the datastore. Record TTL is a datastore-level value, so we can't even re-provide with a shorter TTL.
		return 0, nil
	}
	if !skipDigests {
		dgsts, err := ociClient.AllIdentifiers(ctx, event.Image)
		if err != nil {
			return 0, fmt.Errorf("could not get digests for image %s: %w", event.Image.String(), err)
		}
		keys = append(keys, dgsts...)
	}
	err := router.Advertise(ctx, keys)
	if err != nil {
		return 0, fmt.Errorf("could not advertise image %s: %w", event.Image.String(), err)
	}
	if event.Type == oci.CreateEvent {
		// We don't know how many unique digest keys will be associated with the new image;
		// that can only be updated by the full image list sync in all().
		metrics.AdvertisedImages.WithLabelValues(event.Image.Registry).Add(1)
		if event.Image.Tag == "" {
			metrics.AdvertisedImageDigests.WithLabelValues(event.Image.Registry).Add(1)
		} else {
			metrics.AdvertisedImageTags.WithLabelValues(event.Image.Registry).Add(1)
		}
	}
	return len(keys), nil
}
