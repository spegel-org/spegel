package resilient

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/spegel-org/spegel/internal/option"
)

type unrecoverableError struct {
	error
}

func Unrecoverable(err error) error {
	return &unrecoverableError{err}
}

type DelayFunc func(attempt int, err error) time.Duration

func NoDelay() DelayFunc {
	return func(int, error) time.Duration {
		return 0
	}
}

func FixedDelay(delay time.Duration) DelayFunc {
	return func(int, error) time.Duration {
		return delay
	}
}

func BackoffDelay(start, limit time.Duration) DelayFunc {
	return func(attempt int, err error) time.Duration {
		d := float64(start) * math.Pow(2, float64(attempt-1))
		if d > float64(limit) {
			d = float64(limit)
		}
		return time.Duration(d)
	}
}

type OnRetryFunc func(attempt int, err error)

type RetryConfig struct {
	OnRetry       OnRetryFunc
	LastErrorOnly bool
}

type RetryOption = option.Option[RetryConfig]

func WithOnRetry(onRetry OnRetryFunc) RetryOption {
	return func(cfg *RetryConfig) error {
		cfg.OnRetry = onRetry
		return nil
	}
}

func WithLastErrorOnly() RetryOption {
	return func(cfg *RetryConfig) error {
		cfg.LastErrorOnly = true
		return nil
	}
}

func Retry(ctx context.Context, attempts int, delay DelayFunc, fn func(ctx context.Context) error, opts ...RetryOption) error {
	if fn == nil {
		return errors.New("retry function cannot be nil")
	}

	_, err := RetryValue(ctx, attempts, delay, func(ctx context.Context) (any, error) {
		return nil, fn(ctx)
	}, opts...)
	if err != nil {
		return err
	}
	return nil
}

func RetryValue[T any](ctx context.Context, attempts int, delay DelayFunc, fn func(ctx context.Context) (T, error), opts ...RetryOption) (T, error) {
	var zeroT T

	if delay == nil {
		return zeroT, errors.New("delay cannot be nil")
	}
	if fn == nil {
		return zeroT, errors.New("retry function cannot be nil")
	}

	cfg := RetryConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return zeroT, err
	}

	errs := []error{}
	for {
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		var unrecoverableErr *unrecoverableError
		if errors.As(err, &unrecoverableErr) {
			errs = append(errs, unrecoverableErr.error)
			break
		}
		errs = append(errs, err)
		if attempts > 0 && len(errs) == attempts {
			break
		}

		delayErr := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			d := delay(len(errs), err)
			if d == 0 {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}()
		if delayErr != nil {
			return zeroT, delayErr
		}

		if cfg.OnRetry != nil {
			cfg.OnRetry(len(errs), err)
		}
	}

	if cfg.LastErrorOnly {
		return zeroT, errs[len(errs)-1]
	}
	return zeroT, errors.Join(errs...)
}
