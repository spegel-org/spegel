package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/spegel-org/spegel/pkg/store"
)

func Sync(ctx context.Context, router Router, watcher store.Watcher) error {
	events, eventCh, err := watcher.Watch(ctx)
	if err != nil {
		return err
	}

	// Initial advertisement of all content.
	err = handleEvents(ctx, router, events)
	if err != nil {
		return err
	}

	// Advertise as new events are received.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("event channel closed")
			}
			err := handleEvents(ctx, router, []store.Event{event})
			if err != nil {
				logr.FromContextOrDiscard(ctx).Error(err, "could not handle event")
				continue
			}
		}
	}
}

func handleEvents(ctx context.Context, router Router, events []store.Event) error {
	advertise := []string{}
	withdraw := []string{}
	for _, event := range events {
		switch event.Type {
		case store.CreateEvent:
			if event.Reference != "" {
				advertise = append(advertise, event.Reference)
			}
			if event.Digest != "" {
				advertise = append(advertise, event.Digest.String())
			}
		case store.DeleteEvent:
			if event.Reference != "" {
				withdraw = append(withdraw, event.Reference)
			}
			if event.Digest != "" {
				withdraw = append(withdraw, event.Digest.String())
			}
		default:
			return fmt.Errorf("unhandled event type %s", event.Type)
		}
	}
	err := router.Advertise(ctx, advertise)
	if err != nil {
		return err
	}
	err = router.Withdraw(ctx, withdraw)
	if err != nil {
		return err
	}
	return nil
}
