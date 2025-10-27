package state

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/internal/channel"
	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

type TrackerConfig struct {
	Filters []oci.Filter
}

type TrackerOption = option.Option[TrackerConfig]

func WithRegistryFilters(filters []oci.Filter) TrackerOption {
	return func(cfg *TrackerConfig) error {
		cfg.Filters = filters
		return nil
	}
}

func Track(ctx context.Context, ociStore oci.Store, router routing.Router, opts ...TrackerOption) error {
	cfg := TrackerConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return err
	}

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
			err := tick(ctx, ociStore, router, cfg.Filters)
			if err != nil {
				log.Error(err, "received errors when updating all images")
				continue
			}
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("event channel closed")
			}
			log.Info("OCI event", "ref", event.Reference.String(), "type", event.Type)
			err := handle(ctx, router, event)
			if err != nil {
				log.Error(err, "could not handle event")
				continue
			}
		}
	}
}

func tick(ctx context.Context, ociStore oci.Store, router routing.Router, filters []oci.Filter) error {
	advertisedImages := map[string]float64{}
	advertisedImageDigests := map[string]float64{}
	advertisedImageTags := map[string]float64{}
	advertisedKeys := map[string]float64{}

	imgs, err := ociStore.ListImages(ctx)
	if err != nil {
		return err
	}
	for _, img := range imgs {
		if oci.MatchesFilter(img.Reference, filters) {
			continue
		}
		tagName, ok := img.TagName()
		if ok {
			err := router.Advertise(ctx, []string{tagName})
			if err != nil {
				return err
			}
			advertisedImageTags[img.Registry] += 1
			advertisedKeys[img.Registry] += 1
		}
		advertisedImages[img.Registry] += 1
		advertisedImageDigests[img.Registry] += 1
		advertisedKeys[img.Registry] += 1
	}

	contents, err := ociStore.ListContent(ctx)
	if err != nil {
		return err
	}
	for _, refs := range contents {
		// TODO(phillebaba): Apply filtering on parent image tag.
		if allReferencesMatchFilter(refs, filters) {
			continue
		}
		err := router.Advertise(ctx, []string{refs[0].Digest.String()})
		if err != nil {
			return err
		}
		for _, ref := range refs {
			advertisedKeys[ref.Registry] += 1
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
	err := router.Advertise(ctx, []string{event.Reference.Identifier()})
	if err != nil {
		return err
	}
	return nil
}

func allReferencesMatchFilter(refs []oci.Reference, filters []oci.Filter) bool {
	for _, ref := range refs {
		if !oci.MatchesFilter(ref, filters) {
			return false
		}
	}
	return true
}
