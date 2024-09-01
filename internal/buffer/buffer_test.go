package buffer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBufferPool(t *testing.T) {
	t.Parallel()

	bufferPool := NewBufferPool()
	b := bufferPool.Get()
	require.Len(t, b, 32*1024)
	bufferPool.Put(b)
}
