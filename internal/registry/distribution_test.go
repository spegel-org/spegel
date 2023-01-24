package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnyReference(t *testing.T) {
	tests := []struct {
		name     string
		registry string
		path     string
		expected string
	}{
		{
			name:     "valid manifest tag",
			registry: "example.com",
			path:     "/v2/foo/bar/manifests/hello-world",
			expected: "example.com/foo/bar:hello-world",
		},
		{
			name:     "valid blob digest",
			registry: "docker.io",
			path:     "/v2/library/nginx/blobs/sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369",
			expected: "docker.io/library/nginx@sha256:295c7be079025306c4f1d65997fcf7adb411c88f139ad1d34b537164aa060369",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok, err := AnyReference(tt.registry, tt.path)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tt.expected, ref.String())
		})
	}
}
