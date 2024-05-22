package throttle

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByterateUnmarshalValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected Byterate
	}{
		{
			input:    "1 Bps",
			expected: 1 * Bps,
		},
		{
			input:    "31 KBps",
			expected: 31 * KBps,
		},
		{
			input:    "42 MBps",
			expected: 42 * MBps,
		},
		{
			input:    "120 GBps",
			expected: 120 * GBps,
		},
		{
			input:    "3TBps",
			expected: 3 * TBps,
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			var br Byterate
			err := br.UnmarshalText([]byte(tt.input))
			require.NoError(t, err)
			require.Equal(t, tt.expected, br)
		})
	}
}

func TestByterateUnmarshalInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
	}{
		{
			input: "foobar",
		},
		{
			input: "1 Mbps",
		},
		{
			input: "1.1 MBps",
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			var br Byterate
			err := br.UnmarshalText([]byte(tt.input))
			require.EqualError(t, err, fmt.Sprintf("invalid byterate format %s should be n Bps, n KBps, n MBps, n GBps, or n TBps", tt.input))
		})
	}
}
