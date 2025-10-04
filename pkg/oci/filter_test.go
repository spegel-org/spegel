package oci

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      Reference
		filters  []*regexp.Regexp
		expected bool
	}{
		{
			name: "filters out docker.io",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
			},
			filters:  []*regexp.Regexp{regexp.MustCompile("^docker.io")},
			expected: true,
		},
		{
			name: "does not filter out docker.io",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
			},
			filters:  []*regexp.Regexp{regexp.MustCompile("^ghcr.io")},
			expected: false,
		},
		{
			name: "filters out latest tag",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Tag:        "latest",
			},
			filters:  []*regexp.Regexp{regexp.MustCompile(":latest$")},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, MatchesFilter(tt.ref, tt.filters))
		})
	}
}
