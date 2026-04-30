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

func TestParseMirrorTargets(t *testing.T) {
	t.Parallel()

	t.Run("plain URL", func(t *testing.T) {
		t.Parallel()
		mts, err := parseMirrorTargets([]string{"http://127.0.0.1:5000"})
		require.NoError(t, err)
		require.Len(t, mts, 1)
		require.Equal(t, "http://127.0.0.1:5000", mts[0].URL.String())
		require.False(t, mts[0].OverridePath)
	})

	t.Run("JSON object enables override path", func(t *testing.T) {
		t.Parallel()
		entry := `{"url":"https://123.dkr.ecr.eu-west-1.amazonaws.com/v2/docker-hub","overridePath":true}`
		mts, err := parseMirrorTargets([]string{entry})
		require.NoError(t, err)
		require.Len(t, mts, 1)
		require.Equal(t, "https://123.dkr.ecr.eu-west-1.amazonaws.com/v2/docker-hub", mts[0].URL.String())
		require.True(t, mts[0].OverridePath)
	})

	t.Run("path requires override path", func(t *testing.T) {
		t.Parallel()
		_, err := parseMirrorTargets([]string{"https://example.com/v2/cache"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid registry url path has to be empty")
	})

	t.Run("mixed plain and struct", func(t *testing.T) {
		t.Parallel()
		mts, err := parseMirrorTargets([]string{
			"http://127.0.0.1:5000",
			`{"url":"https://example.com/v2/foo","overridePath":true}`,
		})
		require.NoError(t, err)
		require.Len(t, mts, 2)
		require.False(t, mts[0].OverridePath)
		require.True(t, mts[1].OverridePath)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		_, err := parseMirrorTargets([]string{`{"url":}`})
		require.Error(t, err)
	})

	t.Run("missing url in struct", func(t *testing.T) {
		t.Parallel()
		_, err := parseMirrorTargets([]string{`{"overridePath":true}`})
		require.Error(t, err)
		require.Contains(t, err.Error(), "url is required")
	})
}

func TestMirrorTargetUnmarshalJSON(t *testing.T) {
	t.Parallel()

	t.Run("string form", func(t *testing.T) {
		t.Parallel()
		var mt MirrorTarget
		err := mt.UnmarshalJSON([]byte(`"https://example.com"`))
		require.NoError(t, err)
		require.Equal(t, "https://example.com", mt.URL)
		require.False(t, mt.OverridePath)
	})

	t.Run("object form", func(t *testing.T) {
		t.Parallel()
		var mt MirrorTarget
		err := mt.UnmarshalJSON([]byte(`{"url":"https://example.com/v2/cache","overridePath":true}`))
		require.NoError(t, err)
		require.Equal(t, "https://example.com/v2/cache", mt.URL)
		require.True(t, mt.OverridePath)
	})
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
