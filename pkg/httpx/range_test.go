package httpx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRange(t *testing.T) {
	t.Parallel()

	//nolint: govet // Ignore fieldalignment for readability.
	validTests := []struct {
		value          string
		length         int64
		expectedRange  Range
		expectedString string
		expectedSize   int64
	}{
		{
			value:  "bytes=0-0",
			length: 1,
			expectedRange: Range{
				Start: 0,
				End:   0,
			},
			expectedString: "bytes=0-0",
			expectedSize:   1,
		},
		{
			value:  "bytes=0-499",
			length: 1000,
			expectedRange: Range{
				Start: 0,
				End:   499,
			},
			expectedString: "bytes=0-499",
			expectedSize:   500,
		},
		{
			value:  "bytes=500-999",
			length: 1500,
			expectedRange: Range{
				Start: 500,
				End:   999,
			},
			expectedString: "bytes=500-999",
			expectedSize:   500,
		},
		{
			value:  "bytes=500-",
			length: 1200,
			expectedRange: Range{
				Start: 500,
				End:   1199,
			},
			expectedString: "bytes=500-1199",
			expectedSize:   700,
		},
		{
			value:  "bytes=-200",
			length: 1000,
			expectedRange: Range{
				Start: 800,
				End:   999,
			},
			expectedString: "bytes=800-999",
			expectedSize:   200,
		},
		{
			value:  "bytes=0-1000",
			length: 1000,
			expectedRange: Range{
				Start: 0,
				End:   999,
			},
			expectedString: "bytes=0-999",
			expectedSize:   1000,
		},
		{
			value:  "bytes=999-1000",
			length: 1000,
			expectedRange: Range{
				Start: 999,
				End:   999,
			},
			expectedString: "bytes=999-999",
			expectedSize:   1,
		},
	}
	for _, tt := range validTests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			r, err := ParseRangeHeader(tt.value, tt.length)
			require.NoError(t, err)
			require.Equal(t, tt.expectedRange, r)
			require.Equal(t, tt.expectedString, r.String())
			require.Equal(t, tt.expectedSize, r.Size())
		})
	}

	//nolint: govet // Ignore fieldalignment for readability.
	errorTests := []struct {
		value    string
		length   int64
		expected string
	}{
		{
			value:    "bytes=",
			length:   10,
			expected: "invalid range format",
		},
		{
			value:    "bytes=abc-def",
			length:   10,
			expected: "strconv.ParseInt: parsing \"abc\": invalid syntax",
		},
		{
			value:    "bytes=-0",
			length:   10,
			expected: "invalid suffix range 0",
		},
		{
			value:    "bytes=100-50",
			length:   400,
			expected: "start 100 cannot be larger than end 50",
		},
		{
			value:    "bytes=0-499,500-999",
			length:   1500,
			expected: "multiple ranges not supported",
		},
	}
	for _, tt := range errorTests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			_, err := ParseRangeHeader(tt.value, tt.length)
			require.EqualError(t, err, tt.expected)
		})
	}
}

func TestContentRange(t *testing.T) {
	t.Parallel()

	rng := Range{
		Start: 0,
		End:   50,
	}
	crng := ContentRangeFromRange(rng, 100)
	expected := "bytes 0-50/100"
	require.Equal(t, expected, crng.String())
}
