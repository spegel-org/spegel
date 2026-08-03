package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/pkg/routing"
)

func Track(ctx context.Context, store Store, router routing.Router) error {
	keys, eventCh, err := store.Subscribe(ctx)
	if err != nil {
		return err
	}

	// Initial advertisement of all content.
	err = router.Advertise(ctx, keys)
	if err != nil {
		return err
	}

	// Advertise as new events are received.
	logr.FromContextOrDiscard(ctx).Info("waiting for store events", "store", store.Name())
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

func handleEvent(ctx context.Context, router routing.Router, event Event) error {
	switch event.Type {
	case CreateEvent:
		err := router.Advertise(ctx, []string{event.Key})
		if err != nil {
			return err
		}
		return nil
	case DeleteEvent:
		err := router.Withdraw(ctx, []string{event.Key})
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unhandled event type %s", event.Type)
	}
}
