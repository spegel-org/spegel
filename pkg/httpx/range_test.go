package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRange(t *testing.T) {
	t.Parallel()

	rng, err := ParseRangeHeader(http.Header{}, 0)
	require.NoError(t, err)
	require.Nil(t, rng)

	//nolint: govet // Ignore fieldalignment for readability.
	validTests := []struct {
		value          string
		size           int64
		expectedRange  Range
		expectedString string
		expectedSize   int64
	}{
		{
			value: "bytes=0-0",
			size:  1,
			expectedRange: Range{
				Start: 0,
				End:   0,
			},
			expectedString: "bytes=0-0",
			expectedSize:   1,
		},
		{
			value: "bytes=0-499",
			size:  1000,
			expectedRange: Range{
				Start: 0,
				End:   499,
			},
			expectedString: "bytes=0-499",
			expectedSize:   500,
		},
		{
			value: "bytes=500-999",
			size:  1500,
			expectedRange: Range{
				Start: 500,
				End:   999,
			},
			expectedString: "bytes=500-999",
			expectedSize:   500,
		},
		{
			value: "bytes=500-",
			size:  1200,
			expectedRange: Range{
				Start: 500,
				End:   1199,
			},
			expectedString: "bytes=500-1199",
			expectedSize:   700,
		},
		{
			value: "bytes=-200",
			size:  1000,
			expectedRange: Range{
				Start: 800,
				End:   999,
			},
			expectedString: "bytes=800-999",
			expectedSize:   200,
		},
		{
			value: "bytes=0-1000",
			size:  1000,
			expectedRange: Range{
				Start: 0,
				End:   999,
			},
			expectedString: "bytes=0-999",
			expectedSize:   1000,
		},
		{
			value: "bytes=999-1000",
			size:  1000,
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

			header := http.Header{
				HeaderRange: {tt.value},
			}
			r, err := ParseRangeHeader(header, tt.size)
			require.NoError(t, err)
			require.Equal(t, tt.expectedRange, *r)
			require.Equal(t, tt.expectedString, r.String())
			require.Equal(t, tt.expectedSize, r.Size())
		})
	}

	//nolint: govet // Ignore fieldalignment for readability.
	errorTests := []struct {
		value    string
		size     int64
		expected string
	}{
		{
			value:    "bytes=",
			size:     10,
			expected: "invalid range format",
		},
		{
			value:    "bytes=abc-def",
			size:     10,
			expected: "strconv.ParseInt: parsing \"abc\": invalid syntax",
		},
		{
			value:    "bytes=-0",
			size:     10,
			expected: "invalid suffix range 0",
		},
		{
			value:    "bytes=100-50",
			size:     400,
			expected: "start 100 cannot be larger than end 50",
		},
		{
			value:    "bytes=0-499,500-999",
			size:     1500,
			expected: "multiple ranges not supported",
		},
		{
			value:    "bytes=0-999",
			size:     -1,
			expected: "size -1 cannot be equal or less than zero",
		},
		{
			value:    "foobar=0-1000",
			size:     1000,
			expected: "invalid range unit",
		},
	}
	for _, tt := range errorTests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			header := http.Header{
				HeaderRange: {tt.value},
			}
			_, err := ParseRangeHeader(header, tt.size)
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
