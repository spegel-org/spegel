package oci

import (
	"context"
	"fmt"
	iofs "io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/typeurl/v2"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func TestNewContainerd(t *testing.T) {
	t.Parallel()

	c, err := NewContainerd("socket", "namespace", "foo", nil)
	require.NoError(t, err)
	require.Empty(t, c.contentPath)
	require.Nil(t, c.client)
	require.Equal(t, "foo", c.registryConfigPath)

	c, err = NewContainerd("socket", "namespace", "foo", nil, WithContentPath("local"))
	require.NoError(t, err)
	require.Equal(t, "local", c.contentPath)
}

func TestVerifyStatusResponse(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	err := os.MkdirAll(filepath.Join(tmpDir, "etc", "target", "certs.d"), 0o777)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(tmpDir, "etc", "symlink"), 0o777)
	require.NoError(t, err)
	err = os.Symlink(filepath.Join(tmpDir, "etc", "target", "certs.d"), filepath.Join(tmpDir, "etc", "symlink", "certs.d"))
	require.NoError(t, err)

	tests := []struct {
		name                  string
		requiredConfigPath    string
		expectedErrMsg        string
		configPaths           []string
		discardUnpackedLayers bool
	}{
		{
			name:               "single config path",
			configPaths:        []string{"/etc/containerd/certs.d"},
			requiredConfigPath: "/etc/containerd/certs.d",
		},
		{
			name:               "multiple config paths",
			configPaths:        []string{"/etc/containerd/certs.d", "/etc/docker/certs.d"},
			requiredConfigPath: "/etc/containerd/certs.d",
		},
		{
			name:               "symlinked config path",
			configPaths:        []string{"/etc/target/certs.d"},
			requiredConfigPath: "/etc/symlink/certs.d",
		},
		{
			name:               "empty config path",
			configPaths:        nil,
			requiredConfigPath: "/etc/containerd/certs.d",
			expectedErrMsg:     "Containerd registry config path needs to be set for mirror configuration to take effect",
		},
		{
			name:               "missing single config path",
			configPaths:        []string{"/etc/containerd/certs.d"},
			requiredConfigPath: "/var/lib/containerd/certs.d",
			expectedErrMsg:     fmt.Sprintf("Containerd registry config path is %[1]s/etc/containerd/certs.d but needs to contain path %[1]s/var/lib/containerd/certs.d for mirror configuration to take effect", tmpDir),
		},
		{
			name:               "missing multiple config paths",
			configPaths:        []string{"/etc/containerd/certs.d", "/etc/docker/certs.d"},
			requiredConfigPath: "/var/lib/containerd/certs.d",
			expectedErrMsg:     fmt.Sprintf("Containerd registry config path is %[1]s/etc/containerd/certs.d:%[1]s/etc/docker/certs.d but needs to contain path %[1]s/var/lib/containerd/certs.d for mirror configuration to take effect", tmpDir),
		},
		{
			name:                  "discard unpacked layers enabled",
			discardUnpackedLayers: true,
			expectedErrMsg:        "Containerd discard unpacked layers cannot be enabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpConfigPaths := []string{}
			for _, configPath := range tt.configPaths {
				tmpConfigPaths = append(tmpConfigPaths, filepath.Join(tmpDir, configPath))
			}
			tmpConfigPath := strings.Join(tmpConfigPaths, string(os.PathListSeparator))
			tmpRequiredPath := filepath.Join(tmpDir, tt.requiredConfigPath)
			err := os.MkdirAll(tmpRequiredPath, 0o777)
			require.NoError(t, err)

			resp := &runtimeapi.StatusResponse{
				Info: map[string]string{
					"config": fmt.Sprintf(`{"registry": {"configPath": %q}, "containerd": {"runtimes":{"discardUnpackedLayers": %v}}}`, tmpConfigPath, tt.discardUnpackedLayers),
				},
			}
			err = verifyStatusResponse(resp, tmpRequiredPath)
			if tt.expectedErrMsg != "" {
				require.EqualError(t, err, tt.expectedErrMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestWalkSymbolicLinks(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "data.txt")
	err := os.WriteFile(targetPath, []byte("hello world"), 0o777)
	require.NoError(t, err)
	firstOrderPath := filepath.Join(tmpDir, "first.txt")
	err = os.Symlink(targetPath, firstOrderPath)
	require.NoError(t, err)
	secondOrderPath := filepath.Join(tmpDir, "second.txt")
	err = os.Symlink(firstOrderPath, secondOrderPath)
	require.NoError(t, err)

	// Second order symlink
	paths, err := walkSymbolicLinks(secondOrderPath)
	require.NoError(t, err)
	require.Equal(t, []string{targetPath, firstOrderPath, secondOrderPath}, paths)

	// First order symlink
	paths, err = walkSymbolicLinks(firstOrderPath)
	require.NoError(t, err)
	require.Equal(t, []string{targetPath, firstOrderPath}, paths)

	// No symnlink
	paths, err = walkSymbolicLinks(targetPath)
	require.NoError(t, err)
	require.Equal(t, []string{targetPath}, paths)
}

func TestCreateFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		expectedListFilter  string
		expectedEventFilter string
		registries          []string
	}{
		{
			name:                "only registries",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="^(docker\\.io|gcr\\.io)/"`,
			expectedEventFilter: `topic~="/images/create|/images/update|/images/delete",event.name~="^(docker\\.io|gcr\\.io)/"`,
		},
		{
			name:                "additional image filtes",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="^(docker\\.io|gcr\\.io)/"`,
			expectedEventFilter: `topic~="/images/create|/images/update|/images/delete",event.name~="^(docker\\.io|gcr\\.io)/"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listFilter, eventFilter := createFilters(stringListToUrlList(t, tt.registries))
			require.Equal(t, tt.expectedListFilter, listFilter)
			require.Equal(t, tt.expectedEventFilter, eventFilter)
		})
	}
}

func TestGetEventImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		data              interface{}
		expectedErr       string
		expectedName      string
		expectedEventType EventType
	}{
		{
			name:        "type url is nil",
			data:        nil,
			expectedErr: "any cannot be nil",
		},
		{
			name:        "unknown event",
			data:        &eventtypes.ContainerCreate{},
			expectedErr: "unsupported event type",
		},
		{
			name: "create event",
			data: &eventtypes.ImageCreate{
				Name: "create",
			},
			expectedName:      "create",
			expectedEventType: CreateEvent,
		},
		{
			name: "update event",
			data: &eventtypes.ImageUpdate{
				Name: "update",
			},
			expectedName:      "update",
			expectedEventType: UpdateEvent,
		},
		{
			name: "delete event",
			data: &eventtypes.ImageDelete{
				Name: "delete",
			},
			expectedName:      "delete",
			expectedEventType: DeleteEvent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var e typeurl.Any
			var err error
			if tt.data != nil {
				e, err = typeurl.MarshalAny(tt.data)
				require.NoError(t, err)
			}

			name, event, err := getEventImage(e)
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectedName, name)
			require.Equal(t, tt.expectedEventType, event)
		})
	}
}

func TestMirrorConfiguration(t *testing.T) {
	t.Parallel()

	registryConfigPath := "/etc/containerd/certs.d"

	tests := []struct {
		existingFiles       map[string]string
		expectedFiles       map[string]string
		name                string
		registries          []url.URL
		mirrors             []url.URL
		resolveTags         bool
		createConfigPathDir bool
		appendToBackup      bool
	}{
		{
			name:        "multiple mirros",
			resolveTags: true,
			registries:  stringListToUrlList(t, []string{"http://foo.bar:5000"}),
			mirrors:     stringListToUrlList(t, []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"}),
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']

[host.'http://127.0.0.1:5001']
capabilities = ['pull', 'resolve']
`,
			},
		},
		{
			name:        "resolve tags disabled",
			resolveTags: false,
			registries:  stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:     stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull']
`,
			},
		},
		{
			name:                "config path directory does not exist",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
			},
		},
		{
			name:                "config path directory does exist",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
			},
		},
		{
			name:                "config path directory contains configuration",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": "Hello World",
				"/etc/containerd/certs.d/ghcr.io/hosts.toml":   "Foo Bar",
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "Hello World",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "Foo Bar",
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
			},
		},
		{
			name:                "config path directory contains backup",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "Hello World",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "Foo Bar",
				"/etc/containerd/certs.d/test.txt":                     "test",
				"/etc/containerd/certs.d/foo":                          "bar",
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "Hello World",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "Foo Bar",
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
			},
		},
		{
			name:                "append to existing configuration",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			appendToBackup:      true,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
`,
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
`,
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']

[host.'http://example.com:30020']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']
`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host]
[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			if tt.createConfigPathDir {
				err := fs.Mkdir(registryConfigPath, 0o755)
				require.NoError(t, err)
			}
			for k, v := range tt.existingFiles {
				err := afero.WriteFile(fs, k, []byte(v), 0o644)
				require.NoError(t, err)
			}
			err := AddMirrorConfiguration(context.TODO(), fs, registryConfigPath, tt.registries, tt.mirrors, tt.resolveTags, tt.appendToBackup)
			require.NoError(t, err)
			if len(tt.existingFiles) == 0 {
				ok, err := afero.DirExists(fs, "/etc/containerd/certs.d/_backup")
				require.NoError(t, err)
				require.False(t, ok)
			}
			err = afero.Walk(fs, registryConfigPath, func(path string, fi iofs.FileInfo, _ error) error {
				if fi.IsDir() {
					return nil
				}
				expectedContent, ok := tt.expectedFiles[path]
				require.True(t, ok, path)
				b, err := afero.ReadFile(fs, path)
				require.NoError(t, err)
				require.Equal(t, expectedContent, string(b))
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestMirrorConfigurationInvalidMirrorURL(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	mirrors := stringListToUrlList(t, []string{"http://127.0.0.1:5000"})

	registries := stringListToUrlList(t, []string{"ftp://docker.io"})
	err := AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false)
	require.EqualError(t, err, "invalid registry url scheme must be http or https: ftp://docker.io")

	registries = stringListToUrlList(t, []string{"https://docker.io/foo/bar"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false)
	require.EqualError(t, err, "invalid registry url path has to be empty: https://docker.io/foo/bar")

	registries = stringListToUrlList(t, []string{"https://docker.io?foo=bar"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false)
	require.EqualError(t, err, "invalid registry url query has to be empty: https://docker.io?foo=bar")

	registries = stringListToUrlList(t, []string{"https://foo@docker.io"})
	err = AddMirrorConfiguration(context.TODO(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false)
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
