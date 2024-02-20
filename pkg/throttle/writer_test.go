package throttle

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriter(t *testing.T) {
	br := 500 * Bps
	w := NewWriter(bytes.NewBuffer([]byte{}), br)
	chunkSize := 100
	start := time.Now()
	for i := 0; i < 10; i++ {
		b := make([]byte, chunkSize)
		n, err := w.Write(b)
		require.NoError(t, err)
		require.Equal(t, chunkSize, n)
	}
	d := time.Since(start)
	require.Greater(t, d, 2*time.Second)
	require.Less(t, d, 3*time.Second)
}
