package mirror

import (
	"context"
	"net/url"
	"os"
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestHostFileContent(t *testing.T) {
	registryURL, err := url.Parse("https://example.com")
	require.NoError(t, err)
	mirrorURL, err := url.Parse("http://127.0.0.1:5000")
	require.NoError(t, err)
	content := hostsFileContent(*registryURL, []url.URL{*mirrorURL})
	expected := `server = "https://example.com"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]
[host."http://127.0.0.1:5000".header]
  X-Spegel-Registry = ["https://example.com"]
  X-Spegel-Mirror = ["true"]`
	require.Equal(t, expected, content)
}

func TestHostFileContentMultipleMirrors(t *testing.T) {
	registryURL, err := url.Parse("https://example.com")
	require.NoError(t, err)
	mirrorURLs := stringListToUrlList(t, []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"})
	content := hostsFileContent(*registryURL, mirrorURLs)
	expected := `server = "https://example.com"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]
[host."http://127.0.0.1:5000".header]
  X-Spegel-Registry = ["https://example.com"]
  X-Spegel-Mirror = ["true"]

[host."http://127.0.0.1:5001"]
  capabilities = ["pull", "resolve"]
[host."http://127.0.0.1:5001".header]
  X-Spegel-Registry = ["https://example.com"]
  X-Spegel-Mirror = ["true"]
  X-Spegel-External = ["true"]`
	require.Equal(t, expected, content)
}

func TestHostFileContentDockerOverride(t *testing.T) {
	registryURL, err := url.Parse("https://docker.io")
	require.NoError(t, err)
	mirrorURL, err := url.Parse("http://127.0.0.1:5000")
	require.NoError(t, err)
	content := hostsFileContent(*registryURL, []url.URL{*mirrorURL})
	expected := `server = "https://registry-1.docker.io"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]
[host."http://127.0.0.1:5000".header]
  X-Spegel-Registry = ["https://docker.io"]
  X-Spegel-Mirror = ["true"]`
	require.Equal(t, expected, content)
}

func TestMirrorConfiguration(t *testing.T) {
	fs := afero.NewMemMapFs()
	mirrors := stringListToUrlList(t, []string{"http://127.0.0.1:5000"})

	registryConfigPath := "/etc/containerd/certs.d"
	registries := stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"})
	err := AddMirrorConfiguration(context.TODO(), fs, registryConfigPath, registries, mirrors)
	require.NoError(t, err)
	for _, registry := range registries {
		fp := path.Join(registryConfigPath, registry.Host, "hosts.toml")
		_, err = fs.Stat(fp)
		require.NoError(t, err)
	}
	err = RemoveMirrorConfiguration(context.TODO(), fs, registryConfigPath, registries)
	require.NoError(t, err)
	for _, registry := range registries {
		fp := path.Join(registryConfigPath, registry.Host)
		_, err = fs.Stat(fp)
		require.Error(t, err)
		require.True(t, os.IsNotExist(err))
	}
}

func TestInvalidMirrorURL(t *testing.T) {
	fs := afero.NewMemMapFs()
	mirrors := stringListToUrlList(t, []string{"http://127.0.0.1:5000"})

	registries := stringListToUrlList(t, []string{"ftp://docker.io"})
	err := AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors)
	require.EqualError(t, err, "invalid registry url scheme must be http or https: ftp://docker.io")

	registries = stringListToUrlList(t, []string{"https://docker.io/foo/bar"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors)
	require.EqualError(t, err, "invalid registry url path has to be empty: https://docker.io/foo/bar")

	registries = stringListToUrlList(t, []string{"https://docker.io?foo=bar"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors)
	require.EqualError(t, err, "invalid registry url query has to be empty: https://docker.io?foo=bar")

	registries = stringListToUrlList(t, []string{"https://foo@docker.io"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors)
	require.EqualError(t, err, "invalid registry url user has to be empty: https://foo@docker.io")

}

func stringListToUrlList(t *testing.T, list []string) []url.URL {
	t.Helper()
	urls := []url.URL{}
	for _, item := range list {
		u, err := url.Parse(item)
		require.NoError(t, err)
		urls = append(urls, *u)
	}
	return urls
}
