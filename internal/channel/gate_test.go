package channel

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/testutil"
)

func TestGate(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		gate := NewGate()

		// Gate should start off closed.
		require.False(t, gate.State())
		testutil.RequireChannelReceive(t, gate.WaitFor(false))

		// Gate should block until it is opened.
		start := time.Now()
		go func() {
			time.Sleep(1 * time.Second)
			gate.Open()
		}()
		<-gate.WaitFor(true)
		require.Equal(t, 1*time.Second, time.Since(start))
		require.True(t, gate.State())
	})
}
