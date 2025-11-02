package web

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeb(t *testing.T) {
	t.Parallel()

	w, err := NewWeb(nil, nil, nil, "")
	require.NoError(t, err)
	require.NotNil(t, w.tmpls)
}

func TestFormatBytes(t *testing.T) {
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
			size:     19456,
			expected: "19.0 KB",
		},
		{
			size:     1073741824,
			expected: "1.0 GB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			result := formatBytes(tt.size)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expected string
		duration time.Duration
	}{
		{
			duration: 36 * time.Millisecond,
			expected: "36ms",
		},
		{
			duration: 5 * time.Microsecond,
			expected: "<1ms",
		},
		{
			duration: 5*time.Minute + 128*time.Second,
			expected: "7m 8s",
		},
		{
			duration: 2 * time.Hour,
			expected: "2h",
		},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			result := formatDuration(tt.duration)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestHTMLResponseError_ResponseBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err             error
		name            string
		wantContentType string
		wantBody        []byte
		wantErr         bool
	}{
		{
			name:            "with error",
			err:             errors.New("<error>"),
			wantBody:        []byte(`<p class="error">&lt;error&gt;</p>`),
			wantContentType: "text/plain",
			wantErr:         false,
		},
		{
			name:            "nil error",
			err:             nil,
			wantBody:        nil,
			wantContentType: "",
			wantErr:         true,
		},
		{
			name:            "empty error",
			err:             errors.New(""),
			wantBody:        []byte(`<p class="error"></p>`),
			wantContentType: "text/plain",
			wantErr:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := NewHTMLResponseError(tt.err)
			body, contentType, err := e.ResponseBody()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantBody, body)
			assert.Equal(t, tt.wantContentType, contentType)
		})
	}
}
