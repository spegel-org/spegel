package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/ptr"
)

func TestRange(t *testing.T) {
	t.Parallel()

	rng, err := ParseRangeHeader(http.Header{})
	require.NoError(t, err)
	require.Nil(t, rng)

	validTests := []struct {
		value          string
		expectedRange  Range
		expectedString string
	}{
		{
			value: "bytes=0-0",
			expectedRange: Range{
				Start: ptr.To(int64(0)),
				End:   ptr.To(int64(0)),
			},
			expectedString: "bytes=0-0",
		},
		{
			value: "bytes=0-499",
			expectedRange: Range{
				Start: ptr.To(int64(0)),
				End:   ptr.To(int64(499)),
			},
			expectedString: "bytes=0-499",
		},
		{
			value: "bytes=500-999",
			expectedRange: Range{
				Start: ptr.To(int64(500)),
				End:   ptr.To(int64(999)),
			},
			expectedString: "bytes=500-999",
		},
		{
			value: "bytes=500-",
			expectedRange: Range{
				Start: ptr.To(int64(500)),
				End:   nil,
			},
			expectedString: "bytes=500-",
		},
		{
			value: "bytes=-200",
			expectedRange: Range{
				Start: nil,
				End:   ptr.To(int64(200)),
			},
			expectedString: "bytes=-200",
		},
	}
	for _, tt := range validTests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			header := http.Header{
				HeaderRange: {tt.value},
			}
			r, err := ParseRangeHeader(header)
			require.NoError(t, err)
			require.Equal(t, tt.expectedRange, *r)
			require.Equal(t, tt.expectedString, r.String())
		})
	}

	errorTests := []struct {
		value    string
		expected string
	}{
		{
			value:    "bytes=",
			expected: "invalid range format",
		},
		{
			value:    "bytes=abc-def",
			expected: "strconv.ParseInt: parsing \"abc\": invalid syntax",
		},
		{
			value:    "bytes=-0",
			expected: "suffix range 0 cannot be less than one",
		},
		{
			value:    "bytes=100-50",
			expected: "start 100 cannot be larger than end 50",
		},
		{
			value:    "bytes=0-499,500-999",
			expected: "multiple ranges not supported",
		},
		{
			value:    "foobar=0-1000",
			expected: "invalid range unit",
		},
	}
	for _, tt := range errorTests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			header := http.Header{
				HeaderRange: {tt.value},
			}
			_, err := ParseRangeHeader(header)
			require.EqualError(t, err, tt.expected)
		})
	}

	err = Range{Start: ptr.To(int64(-1)), End: ptr.To(int64(0))}.Validate()
	require.EqualError(t, err, "start range -1 cannot be less than zero")
	err = Range{Start: ptr.To(int64(0)), End: ptr.To(int64(-1))}.Validate()
	require.EqualError(t, err, "end range -1 cannot be less than zero")
}

func TestContentRange(t *testing.T) {
	t.Parallel()

	//nolint: govet // Ignore fieldalignment for readability.
	tests := []struct {
		value          string
		size           int64
		expectedString string
		expectedLength int64
	}{
		{
			value:          "bytes=0-0",
			size:           1000,
			expectedString: "bytes 0-0/1000",
			expectedLength: 1,
		},
		{
			value:          "bytes=0-9",
			size:           1000,
			expectedString: "bytes 0-9/1000",
			expectedLength: 10,
		},
		{
			value:          "bytes=500-",
			size:           1000,
			expectedString: "bytes 500-999/1000",
			expectedLength: 500,
		},
		{
			value:          "bytes=-500",
			size:           1000,
			expectedString: "bytes 500-999/1000",
			expectedLength: 500,
		},
		{
			value:          "bytes=-1000",
			size:           1000,
			expectedString: "bytes 0-999/1000",
			expectedLength: 1000,
		},
		{
			value:          "bytes=0-999",
			size:           1000,
			expectedString: "bytes 0-999/1000",
			expectedLength: 1000,
		},
		{
			value:          "bytes=900-1500",
			size:           1000,
			expectedString: "bytes 900-999/1000",
			expectedLength: 100,
		},
		{
			value:          "bytes=-1",
			size:           1000,
			expectedString: "bytes 999-999/1000",
			expectedLength: 1,
		},
		{
			value:          "bytes=100-199",
			size:           1000,
			expectedString: "bytes 100-199/1000",
			expectedLength: 100,
		},
		{
			value:          "bytes=0-",
			size:           1000,
			expectedString: "bytes 0-999/1000",
			expectedLength: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			header := http.Header{
				HeaderRange: {tt.value},
			}
			rng, err := ParseRangeHeader(header)
			require.NoError(t, err)
			crng, err := ContentRangeFromRange(*rng, tt.size)
			require.NoError(t, err)
			require.Equal(t, tt.expectedString, crng.String())
			require.Equal(t, tt.expectedLength, crng.Length())
		})
	}

	_, err := ContentRangeFromRange(Range{}, -1)
	require.EqualError(t, err, "size -1 cannot be equal or less than zero")

	_, err = ContentRangeFromRange(Range{Start: nil, End: nil}, 100)
	require.EqualError(t, err, "start and end range cannot both be empty")
}
