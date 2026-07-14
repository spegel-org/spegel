package containerd

import (
	"testing"

	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/oci"
)

func TestContentLabelsToReferences(t *testing.T) {
	t.Parallel()

	dgst := digest.Digest("foo")
	tests := []struct {
		name     string
		labels   map[string]string
		expected []oci.Reference
	}{
		{
			name: "one matching",
			labels: map[string]string{
				"containerd.io/distribution.source.docker.io": "library/alpine",
			},
			expected: []oci.Reference{
				{
					Registry:   "docker.io",
					Repository: "library/alpine",
					Digest:     dgst,
				},
			},
		},
		{
			name: "multiple matching",
			labels: map[string]string{
				"containerd.io/distribution.source.example.com": "foo",
				"containerd.io/distribution.source.ghcr.io":     "spegel-org/spegel",
			},
			expected: []oci.Reference{
				{
					Registry:   "ghcr.io",
					Repository: "spegel-org/spegel",
					Digest:     dgst,
				},
				{
					Registry:   "example.com",
					Repository: "foo",
					Digest:     dgst,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			t.Parallel()

			refs, err := contentLabelsToReferences(tt.labels, dgst)
			require.NoError(t, err)
			require.ElementsMatchT(t, tt.expected, refs)
		})
	}

	_, err := contentLabelsToReferences(map[string]string{}, dgst)
	require.EqualError(t, err, "no distribution source labels found for foo")
}
