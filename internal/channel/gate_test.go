package channel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGate(t *testing.T) {
	t.Parallel()

	g := NewGate()

	for range 3 {
		require.False(t, g.IsOpen())
		select {
		case <-g.Wait():
			require.FailNow(t, "wait should be blocking")
		default:
		}
		g.Set(false)
	}

	for range 3 {
		g.Set(true)
		require.True(t, g.IsOpen())
		select {
		case <-g.Wait():
		default:
			require.FailNow(t, "wait should not be blocking")
		}
	}
}
