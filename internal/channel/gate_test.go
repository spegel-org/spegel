package channel

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-openapi/testify/v2/require"

	"github.com/spegel-org/spegel/internal/testutil"
)

func TestGate(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		gate := NewGate()

		// Gate should start off closed.
		require.FalseT(t, gate.State())
		testutil.RequireChannelReceive(t, gate.WaitFor(false))

		// Gate should block until it is opened.
		start := time.Now()
		go func() {
			time.Sleep(1 * time.Second)
			gate.Open()
		}()
		<-gate.WaitFor(true)
		require.EqualT(t, 1*time.Second, time.Since(start))
		require.TrueT(t, gate.State())
	})
}
