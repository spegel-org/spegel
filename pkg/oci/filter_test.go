package oci

import (
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      Reference
		filters  []Filter
		expected bool
	}{
		{
			name: "filters out docker.io",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
			},
			filters:  []Filter{RegexFilter{regexp.MustCompile("^docker.io")}},
			expected: true,
		},
		{
			name: "does not filter out docker.io",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
			},
			filters:  []Filter{RegexFilter{regexp.MustCompile("^ghcr.io")}},
			expected: false,
		},
		{
			name: "filters out latest tag",
			ref: Reference{
				Registry:   "docker.io",
				Repository: "library/ubuntu",
				Tag:        "latest",
			},
			filters:  []Filter{RegexFilter{regexp.MustCompile(":latest$")}},
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

func TestFilterForMirroredRegistries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		registries []string
	}{
		{
			name:       "empty mirrored registries",
			registries: []string{},
		},
		{
			name:       "wildcard registries",
			registries: []string{"https://docker.io", "*"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			filter, err := FilterForMirroredRegistries(tt.registries)
			require.NoError(t, err)
			require.Nil(t, filter)
		})
	}

	registries := []string{"https://docker.io", "https://quay.io", "http://localhost:5000"}
	filter, err := FilterForMirroredRegistries(registries)
	require.NoError(t, err)
	expected := []string{"docker.io", "quay.io", "localhost:5000"}
	require.Equal(t, expected, filter.Whitelist)

	ref := Reference{
		Registry:   "localhost:6000",
		Repository: "foo",
		Tag:        "bar",
	}
	matches := MatchesFilter(ref, []Filter{filter})
	require.True(t, matches)

	ref = Reference{
		Registry:   "localhost:5000",
		Repository: "foo",
		Tag:        "bar",
	}
	matches = MatchesFilter(ref, []Filter{filter})
	require.False(t, matches)

	ref = Reference{
		Registry:   "docker.io",
		Repository: "foo",
		Tag:        "bar",
	}
	matches = MatchesFilter(ref, []Filter{filter})
	require.False(t, matches)
}

func TestParseRegistries(t *testing.T) {
	t.Parallel()

	registries := []string{"https://docker.io", "_default", "http://localhost:9090"}
	rus, err := parseRegistries(registries, true)
	require.NoError(t, err)
	strs := []string{}
	for _, ru := range rus {
		strs = append(strs, ru.String())
	}
	expected := []string{"https://docker.io", "//_default", "http://localhost:9090"}
	require.Equal(t, expected, strs)

	//nolint: govet // Prioritize readability in tests.
	fail := []struct {
		name          string
		registries    []string
		allowWildcard bool
		expected      string
	}{
		{
			name:          "two wildcards",
			registries:    []string{"*", "_default"},
			allowWildcard: true,
			expected:      "registries should not contain two wildcards",
		},
		{
			name:          "wildcard when not allowed",
			registries:    []string{"*"},
			allowWildcard: false,
			expected:      "wildcard registries are not allowed",
		},
	}
	for _, tt := range fail {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseRegistries(tt.registries, tt.allowWildcard)
			require.EqualError(t, err, tt.expected)
		})
	}
}

func TestValidateRegistryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		urlStr   string
		expected string
	}{
		{
			urlStr:   "ftp://docker.io",
			expected: "invalid registry url scheme must be http or https: ftp://docker.io",
		},
		{
			urlStr:   "https://docker.io/foo/bar",
			expected: "invalid registry url path has to be empty: https://docker.io/foo/bar",
		},
		{
			urlStr:   "https://docker.io?foo=bar",
			expected: "invalid registry url query has to be empty: https://docker.io?foo=bar",
		},
		{
			urlStr:   "https://foo@docker.io",
			expected: "invalid registry url user has to be empty: https://foo@docker.io",
		},
	}
	for _, tt := range tests {
		t.Run(tt.urlStr, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(tt.urlStr)
			require.NoError(t, err)
			err = validateRegistryURL(u)
			require.EqualError(t, err, tt.expected)
		})
	}

	u, err := url.Parse("https://docker.io")
	require.NoError(t, err)
	err = validateRegistryURL(u)
	require.NoError(t, err)
}
