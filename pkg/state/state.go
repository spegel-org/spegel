package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func Track(ctx context.Context, ociStore oci.Store, router routing.Router) error {
	initial, eventCh, err := ociStore.Subscribe(ctx)
	if err != nil {
		return err
	}

	// Initial advertisement of all content.
	keys := []string{}
	for img, dgsts := range initial {
		// if !oci.MatchesFilter(img.Reference, cfg.Filters) {
		tagName, ok := img.TagName()
		if ok {
			metrics.AdvertisedImageTags.WithLabelValues(img.Registry).Inc()
			keys = append(keys, tagName)
		}
		// }
		metrics.AdvertisedImageDigests.WithLabelValues(img.Registry).Inc()
		for _, dgst := range dgsts {
			metrics.AdvertisedContentDigests.WithLabelValues(img.Registry).Inc()
			keys = append(keys, dgst.String())
		}
	}
	err = router.Advertise(ctx, keys)
	if err != nil {
		return err
	}

	// Advertise as new events are received.
	logr.FromContextOrDiscard(ctx).Info("waiting for store events")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("event channel closed")
			}
			err := handleEvent(ctx, router, event)
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "could not handle event")
				continue
			}
		}
	}
}

func handleEvent(ctx context.Context, router routing.Router, event oci.OCIEvent) error {
	logr.FromContextOrDiscard(ctx).Info("OCI event", "ref", event.Reference.String(), "type", event.Type)
	switch event.Type {
	case oci.CreateEvent:
		if event.Reference.Tag != "" {
			metrics.AdvertisedImageTags.WithLabelValues(event.Reference.Registry).Inc()
		} else {
			metrics.AdvertisedContentDigests.WithLabelValues(event.Reference.Registry).Inc()
		}
		err := router.Advertise(ctx, []string{event.Reference.Identifier()})
		if err != nil {
			return err
		}
		return nil
	case oci.DeleteEvent:
		if event.Reference.Tag != "" {
			metrics.AdvertisedImageTags.WithLabelValues(event.Reference.Registry).Dec()
		} else {
			metrics.AdvertisedContentDigests.WithLabelValues(event.Reference.Registry).Dec()
		}
		err := router.Withdraw(ctx, []string{event.Reference.Identifier()})
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unhandled event type %s", event.Type)
	}
}
