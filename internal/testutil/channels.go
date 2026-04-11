package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func RequireChannelReceive[T any](t *testing.T, ch <-chan T) {
	t.Helper()

	select {
	case _, ok := <-ch:
		if !ok {
			require.FailNow(t, "channel is closed but expected to receive")
		}
	default:
		require.FailNow(t, "channel does not have value to receive")
	}
}

func RequireChannelOpen[T any](t *testing.T, ch <-chan T) {
	t.Helper()

	select {
	case _, ok := <-ch:
		if ok {
			require.FailNow(t, "channel is receiving values but expected to be open blocking")
		}
		require.FailNow(t, "channel is closed but expected to be open blocking")
	default:
	}
}

func RequireChannelClosed[T any](t *testing.T, ch <-chan T) {
	t.Helper()

	select {
	case _, ok := <-ch:
		if ok {
			require.FailNow(t, "channel is receiving values but expected to be closed")
		}
	default:
		require.FailNow(t, "channel is open but expected to be closed")
	}
}
