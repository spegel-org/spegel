package httpx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatRangeHeader(t *testing.T) {
	t.Parallel()

	br := ByteRange{Start: 10, End: 2000}
	val := FormatRangeHeader(br)
	require.Equal(t, "bytes=10-2000", val)
}

func TestFormatMultipartRangeHeader(t *testing.T) {
	t.Parallel()

	brr := []ByteRange{
		{
			Start: 10,
			End:   100,
		},
		{
			Start: 0,
			End:   1,
		},
	}
	val := FormatMultipartRangeHeader(brr)
	require.Equal(t, "bytes=10-100, 0-1", val)

	val = FormatMultipartRangeHeader(nil)
	require.Empty(t, val)
}
