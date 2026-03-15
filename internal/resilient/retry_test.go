package resilient

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDelayFunc(t *testing.T) {
	t.Parallel()

	expected := 13 * time.Millisecond
	d := FixedDelay(expected)(10, nil)
	require.Equal(t, expected, d)

	delay := BackoffDelay(10*time.Millisecond, 140*time.Millisecond)
	expectedDurations := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		140 * time.Millisecond,
	}
	for i := range 5 {
		d := delay(i+1, fmt.Errorf("%d", i))
		require.Equal(t, expectedDurations[i], d)
	}
}

func TestRetry(t *testing.T) {
	t.Parallel()

	delay := FixedDelay(0)

	err := Retry(t.Context(), 0, nil, func(ctx context.Context) error { return nil })
	require.EqualError(t, err, "delay cannot be nil")
	err = Retry(t.Context(), 0, delay, nil)
	require.EqualError(t, err, "retry function cannot be nil")
	_, err = RetryValue[string](t.Context(), 0, delay, nil)
	require.EqualError(t, err, "retry function cannot be nil")

	optErr := errors.New("option error")
	retryOpts := []RetryOption{
		func(cfg *RetryConfig) error {
			return optErr
		},
	}
	err = Retry(t.Context(), 0, delay, func(ctx context.Context) error { return nil }, retryOpts...)
	require.ErrorIs(t, err, optErr)

	err = Retry(t.Context(), 0, delay, func(ctx context.Context) error {
		return nil
	})
	require.NoError(t, err)

	expected := errors.New("fail")
	err = Retry(t.Context(), 3, delay, func(ctx context.Context) error {
		return expected
	})
	require.ErrorIs(t, expected, err)

	expected = errors.New("unrecoverable")
	i := 0
	err = Retry(t.Context(), 0, delay, func(ctx context.Context) error {
		if i == 3 {
			return Unrecoverable(expected)
		}
		i += 1
		return errors.New("retry error")
	})
	require.ErrorIs(t, expected, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = Retry(ctx, 2, delay, func(ctx context.Context) error {
		return errors.New("retry error")
	})
	require.ErrorIs(t, err, context.Canceled)

	retries := 3
	count := 0
	retryOpts = []RetryOption{
		WithOnRetry(func(attempt int, err error) {
			count += 1
		}),
	}
	err = Retry(t.Context(), retries, delay, func(ctx context.Context) error {
		if count == 2 {
			return nil
		}
		return errors.New("retry error")
	}, retryOpts...)
	require.NoError(t, err)
	require.Equal(t, retries-1, count)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel = context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		err = Retry(ctx, 0, FixedDelay(500*time.Millisecond), func(ctx context.Context) error {
			return errors.New("retry error")
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
