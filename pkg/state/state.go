package state

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/afero"

	"github.com/spegel-org/spegel/internal/channel"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

var runtimeRegistryUrls []url.URL

func Track(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
	log := logr.FromContextOrDiscard(ctx)
	eventCh, errCh, err := ociClient.Subscribe(ctx)
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
			return nil
		case <-tickerCh:
			log.Info("running scheduled image state update")

			if err := all(ctx, ociClient, router, resolveLatestTag); err != nil {
				log.Error(err, "received errors when updating all images")
				continue
			}
		case event, ok := <-eventCh:
			if !ok {
				return errors.New("image event channel closed")
			}
			log.Info("received image event", "image", event.Image.String(), "type", event.Type)
			if _, err := update(ctx, ociClient, router, event, false, resolveLatestTag); err != nil {
				log.Error(err, "received error when updating image")
				continue
			}
		case err, ok := <-errCh:
			if !ok {
				return errors.New("image error channel closed")
			}
			log.Error(err, "event channel error")
		}
	}
}

func TrackConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs, mirrorURLs []url.URL, resolveTags bool, registriesFilePath string, refreshInterval time.Duration) error {
	log := logr.FromContextOrDiscard(ctx)

	immediateCh := make(chan time.Time, 1)
	immediateCh <- time.Now()
	close(immediateCh)
	expirationTicker := time.NewTicker(refreshInterval)
	defer expirationTicker.Stop()
	tickerCh := channel.Merge(immediateCh, expirationTicker.C)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tickerCh:
			if registriesFilePath == "" {
				log.Info("registries file path not set, skip")
				continue
			}

			log.Info("running scheduled configuration update")
			if err := updateConfiguration(ctx, fs, configPath, registryURLs, mirrorURLs, resolveTags, registriesFilePath); err != nil {
				log.Error(err, "received errors when updating configuration")
				continue
			}
		}
	}
}

func updateConfiguration(ctx context.Context, fs afero.Fs, configPath string, configuredRegistryURLs, mirrorURLs []url.URL, resolveTags bool, registriesFilePath string) error {
	log := logr.FromContextOrDiscard(ctx)
	if registriesFilePath == "" {
		log.Info("registries file path not set, skip")
		return nil
	}
	files, err := afero.ReadDir(fs, registriesFilePath)
	if errors.Is(err, afero.ErrFileNotFound) {
		log.Info("registries file not found, skip", "path", registriesFilePath)
		return nil
	}
	if err != nil {
		return err
	}

	var currentRuntimeRegistryUrls []url.URL

	for _, file := range files {
		// skip hidden files
		if strings.HasPrefix(file.Name(), "..") {
			continue
		}
		urlRaw := "https://" + file.Name()
		parsedURL, err := url.Parse(urlRaw)
		if err != nil {
			log.Error(err, "failed to parse registry url", "url", urlRaw)
			continue
		}

		currentRuntimeRegistryUrls = append(currentRuntimeRegistryUrls, *parsedURL)
	}

	if isRuntimeURLArrayUpdated(currentRuntimeRegistryUrls, runtimeRegistryUrls) {
		urlsNeedUpdate := MergeArrays(currentRuntimeRegistryUrls, configuredRegistryURLs)
		log.Info("Need to update runtime registries")

		err := oci.AddMirrorConfiguration(ctx, fs, configPath, urlsNeedUpdate, mirrorURLs, resolveTags, false)
		if err != nil {
			return err
		}

		runtimeRegistryUrls = currentRuntimeRegistryUrls
	}

	return nil
}

func MergeArrays(arr1, arr2 []url.URL) []url.URL {
	unique := make(map[url.URL]struct{})
	for _, item := range append(arr1, arr2...) {
		unique[item] = struct{}{}
	}

	result := make([]url.URL, 0, len(unique))
	for key := range unique {
		result = append(result, key)
	}
	return result
}

func isRuntimeURLArrayUpdated(arr1, arr2 []url.URL) bool {
	if len(arr1) != len(arr2) {
		return true
	}

	sortedArr1 := convertURLArrayToStringArray(arr1)
	sortedArr2 := convertURLArrayToStringArray(arr2)

	sort.Strings(sortedArr1)
	sort.Strings(sortedArr2)

	for i := range sortedArr1 {
		if sortedArr1[i] != sortedArr2[i] {
			return true
		}
	}
	return false
}

// convertURLArrayToStringArray converts []url.URL to []string
func convertURLArrayToStringArray(urls []url.URL) []string {
	result := make([]string, len(urls))
	for i, u := range urls {
		result[i] = u.String()
	}
	return result
}

func all(ctx context.Context, ociClient oci.Client, router routing.Router, resolveLatestTag bool) error {
	log := logr.FromContextOrDiscard(ctx).V(4)
	imgs, err := ociClient.ListImages(ctx)
	if err != nil {
		return err
	}

	// TODO: Update metrics on subscribed events. This will require keeping state in memory to know about key count changes.
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
		log.Info("sync image event", "image", event.Image.String(), "type", event.Type)
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
		if tagName, ok := event.Image.TagName(); ok {
			keys = append(keys, tagName)
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
		dgsts, err := oci.WalkImage(ctx, ociClient, event.Image)
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
