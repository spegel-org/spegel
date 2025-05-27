package ocifs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

type OCIFile interface {
	io.ReadCloser

	Descriptor() (ocispec.Descriptor, error)
}

var _ OCIFile = &RoutedFile{}

type RoutedFile struct {
	fetch func() (*http.Response, error)
}

func NewRoutedFile(ctx context.Context, router routing.Router, dist oci.DistributionPath, method string, forwardScheme string, timeout time.Duration, retries int) (*RoutedFile, error) {
	//nolint:bodyclose // Response body is only closed in Close().
	fetch := sync.OnceValues(func() (*http.Response, error) {
		resolveCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		peerCh, err := router.Resolve(resolveCtx, dist.Reference(), retries)
		if err != nil {
			return nil, err
		}

		u := dist.URL()
		u.Scheme = forwardScheme

		mirrorAttempts := 0
		for {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("routing for OCI file  has been cancelled: %w", ctx.Err())
			case peer, ok := <-peerCh:
				// Channel closed means no more mirrors will be received and max retries has been reached.
				if !ok {
					err = fmt.Errorf("mirror with OCI file %s could not be found", dist.Reference())
					if mirrorAttempts > 0 {
						err = errors.Join(err, fmt.Errorf("requests to %d mirrors failed, all attempts have been exhausted or timeout has been reached", mirrorAttempts))
					}
					return nil, err
				}

				u.Host = peer.String()
				resp, err := doRequest(ctx, http.DefaultClient, method, u)
				if err != nil {
					logr.FromContextOrDiscard(ctx).Error(err, "request to mirror failed", "attempt", mirrorAttempts, "path", u.Path, "mirror", peer)
					continue
				}
				return resp, nil
			}
		}
	})
	f := &RoutedFile{
		fetch: fetch,
	}
	return f, nil
}

func (f *RoutedFile) Read(p []byte) (n int, err error) {
	//nolint:bodyclose // Response body is only closed in Close().
	resp, err := f.fetch()
	if err != nil {
		return 0, err
	}
	return resp.Body.Read(p)
}

func (f *RoutedFile) Close() error {
	resp, err := f.fetch()
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	return errors.Join(closeErr, copyErr)
}

func (f *RoutedFile) Descriptor() (ocispec.Descriptor, error) {
	//nolint:bodyclose // Response body is only closed in Close().
	resp, err := f.fetch()
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	desc, err := oci.DescriptorFromHeader(resp.Header)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

func doRequest(ctx context.Context, client *http.Client, method string, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Spegel-Mirrored", "true")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	//nolint:staticcheck // Ignore until replaced with status error.
	if !(resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
		return nil, fmt.Errorf("expected status code %s but got %s", http.StatusText(http.StatusOK), resp.Status)
	}
	return resp, nil
}
