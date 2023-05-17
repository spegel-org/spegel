package utils

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func StringListToUrlList(t *testing.T, list []string) []url.URL {
	t.Helper()
	urls := []url.URL{}
	for _, item := range list {
		u, err := url.Parse(item)
		require.NoError(t, err)
		urls = append(urls, *u)
	}
	return urls
}
