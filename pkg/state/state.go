package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"

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

	// Start subscribing to not miss events.
	eventCh, err := ociStore.Subscribe(ctx)
	if err != nil {
		return err
	}

	// Initial advertisement of all content.
	keys := []string{}
	imgs, err := ociStore.ListImages(ctx)
	if err != nil {
		return err
	}
	for _, img := range imgs {
		if oci.MatchesFilter(img.Reference, cfg.Filters) {
			continue
		}
		tagName, ok := img.TagName()
		if ok {
			keys = append(keys, tagName)
			metrics.AdvertisedImageTags.WithLabelValues(img.Registry).Inc()
			metrics.AdvertisedKeys.WithLabelValues(img.Registry).Inc()
		}
		metrics.AdvertisedImages.WithLabelValues(img.Registry).Inc()
		metrics.AdvertisedImageDigests.WithLabelValues(img.Registry).Inc()
		metrics.AdvertisedKeys.WithLabelValues(img.Registry).Inc()
	}
	contents, err := ociStore.ListContent(ctx)
	if err != nil {
		return err
	}
	for _, refs := range contents {
		// TODO(phillebaba): Apply filtering on parent image tag.
		if allReferencesMatchFilter(refs, cfg.Filters) {
			continue
		}
		for _, ref := range refs {
			metrics.AdvertisedKeys.WithLabelValues(ref.Registry).Inc()
		}
		keys = append(keys, refs[0].Digest.String())
	}
	err = router.Advertise(ctx, keys)
	if err != nil {
		return err
	}

	// Watch for OCI events.
	log := logr.FromContextOrDiscard(ctx)
	log.Info("waiting for store events")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("event channel closed")
			}
			log.Info("OCI event", "ref", event.Reference.String(), "type", event.Type)
			err := handleEvent(ctx, router, event)
			if err != nil {
				log.Error(err, "could not handle event")
				continue
			}
		}
	}
}

func handleEvent(ctx context.Context, router routing.Router, event oci.OCIEvent) error {
	switch event.Type {
	case oci.CreateEvent:
		err := router.Advertise(ctx, []string{event.Reference.Identifier()})
		if err != nil {
			return err
		}
		return nil
	case oci.DeleteEvent:
		err := router.Withdraw(ctx, []string{event.Reference.Identifier()})
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unhandled event type %s", event.Type)
	}
}

func allReferencesMatchFilter(refs []oci.Reference, filters []oci.Filter) bool {
	for _, ref := range refs {
		if !oci.MatchesFilter(ref, filters) {
			return false
		}
	}
	return true
}
