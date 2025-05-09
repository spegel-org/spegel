package web

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatByteSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expected string
		size     int64
	}{
		{
			size:     1,
			expected: "1 B",
		},
		{
			size:     18954,
			expected: "19.0 kB",
		},
		{
			size:     1000000000,
			expected: "1.0 GB",
		},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatInt(tt.size, 10), func(t *testing.T) {
			t.Parallel()

			result := formatByteSize(tt.size)
			require.Equal(t, tt.expected, result)
		})
	}
}
