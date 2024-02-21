package throttle

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestWriter(t *testing.T) {
	limit := rate.Limit(500 * Bps)
	limiter := rate.NewLimiter(limit, 1024*1024)
	limiter.AllowN(time.Now(), 1024*1024)
	w := NewWriter(bytes.NewBuffer([]byte{}), limiter)
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
