package oci

import (
	"fmt"
	iofs "io/fs"
	"maps"
	"net/url"
	"path/filepath"
	"testing"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
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

func TestCanVerifyContainerdConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version  string
		expected bool
	}{
		{
			version:  "v2.0.2",
			expected: false,
		},
		{
			version:  "2.1.4",
			expected: false,
		},
		{
			version:  "v1.7.27",
			expected: true,
		},
		{
			version:  "1.6.0",
			expected: true,
		},
	}
	for _, tt := range tests {
		// Testing with a suffix is important as some Linux distributions will modify the version
		// with a non Semver compliant modification. Even if the version is supposed to comply with
		// semver that may not always be the case.
		for _, suffix := range []string{"", "~ds1"} {
			version := tt.version + suffix
			t.Run(version, func(t *testing.T) {
				t.Parallel()

				ok, err := canVerifyContainerdConfiguration(tt.version)
				require.NoError(t, err)
				require.Equal(t, tt.expected, ok)
			})
		}
	}
}

func TestVerifyStatusResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		configPath            string
		requiredConfigPath    string
		expectedErrMsg        string
		discardUnpackedLayers bool
	}{
		{
			name:               "empty config path",
			configPath:         "",
			requiredConfigPath: "/etc/containerd/certs.d",
			expectedErrMsg:     "Containerd registry config path needs to be set for mirror configuration to take effect",
		},
		{
			name:               "single config path",
			configPath:         "/etc/containerd/certs.d",
			requiredConfigPath: "/etc/containerd/certs.d",
		},
		{
			name:               "missing single config path",
			configPath:         "/etc/containerd/certs.d",
			requiredConfigPath: "/var/lib/containerd/certs.d",
			expectedErrMsg:     "Containerd registry config path is /etc/containerd/certs.d but needs to contain path /var/lib/containerd/certs.d for mirror configuration to take effect",
		},
		{
			name:               "multiple config paths",
			configPath:         "/etc/containerd/certs.d:/etc/docker/certs.d",
			requiredConfigPath: "/etc/containerd/certs.d",
		},
		{
			name:               "missing multiple config paths",
			configPath:         "/etc/containerd/certs.d:/etc/docker/certs.d",
			requiredConfigPath: "/var/lib/containerd/certs.d",
			expectedErrMsg:     "Containerd registry config path is /etc/containerd/certs.d:/etc/docker/certs.d but needs to contain path /var/lib/containerd/certs.d for mirror configuration to take effect",
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

			resp := &runtimeapi.StatusResponse{
				Info: map[string]string{
					"config": fmt.Sprintf(`{"registry": {"configPath": %q}, "containerd": {"discardUnpackedLayers": %v}}`, tt.configPath, tt.discardUnpackedLayers),
				},
			}
			err := verifyStatusResponse(resp, tt.requiredConfigPath)
			if tt.expectedErrMsg != "" {
				require.EqualError(t, err, tt.expectedErrMsg)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyStatusResponseMissingRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         string
		expectedErrMsg string
	}{
		{
			name:           "missing discard upacked layers false",
			config:         `{"registry": {"configPath": "foo"}, "containerd": {"runtimes":{"discardUnpackedLayers": false}}}`,
			expectedErrMsg: "field containerd.discardUnpackedLayers missing from config",
		},
		{
			name:           "missing discard upacked layers true",
			config:         `{"registry": {"configPath": "foo"}, "containerd": {"runtimes":{"discardUnpackedLayers": true}}}`,
			expectedErrMsg: "field containerd.discardUnpackedLayers missing from config",
		},
		{
			name:           "missing containerd field",
			config:         `{"registry": {"configPath": "foo"}}`,
			expectedErrMsg: "field containerd.discardUnpackedLayers missing from config",
		},
		{
			name:           "missing registry field",
			config:         `{"containerd": {"discardUnpackedLayers": false}}`,
			expectedErrMsg: "field registry.configPath missing from config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &runtimeapi.StatusResponse{
				Info: map[string]string{
					"config": tt.config,
				},
			}
			err := verifyStatusResponse(resp, "foo")
			require.EqualError(t, err, tt.expectedErrMsg)
		})
	}
}

func TestBackupConfig(t *testing.T) {
	t.Parallel()

	log := logr.Discard()

	fs := afero.NewMemMapFs()
	err := fs.MkdirAll("/config", 0o755)
	require.NoError(t, err)
	err = backupConfig(log, fs, "/config")
	require.NoError(t, err)
	ok, err := afero.DirExists(fs, "/config/_backup/")
	require.NoError(t, err)
	require.True(t, ok)
	files, err := afero.ReadDir(fs, "/config/_backup")
	require.NoError(t, err)
	require.Empty(t, files)

	fs = afero.NewMemMapFs()
	err = fs.MkdirAll("/config", 0o755)
	require.NoError(t, err)
	err = afero.WriteFile(fs, "/config/test.txt", []byte("Hello World"), 0o644)
	require.NoError(t, err)
	err = backupConfig(log, fs, "/config/")
	require.NoError(t, err)
	ok, err = afero.DirExists(fs, "/config/_backup/")
	require.NoError(t, err)
	require.True(t, ok)
	files, err = afero.ReadDir(fs, "/config/_backup/")
	require.NoError(t, err)
	require.Len(t, files, 1)
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
			name:                "with registry filtering",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="^(docker\\.io|gcr\\.io)/"`,
			expectedEventFilter: `topic~="/images/create|/images/update|/images/delete",event.name~="^(docker\\.io|gcr\\.io)/"`,
		},
		{
			name:                "without registry filtering",
			registries:          []string{},
			expectedListFilter:  `name~="^.+/"`,
			expectedEventFilter: `topic~="/images/create|/images/update|/images/delete",event.name~="^.+/"`,
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
		data              any
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
		username            string
		password            string
		registries          []url.URL
		mirrors             []url.URL
		resolveTags         bool
		createConfigPathDir bool
		prependExisting     bool
	}{
		{
			name:            "multiple mirrors",
			resolveTags:     true,
			registries:      stringListToUrlList(t, []string{"http://foo.bar:5000"}),
			mirrors:         stringListToUrlList(t, []string{"http://127.0.0.1:5000", "http://127.0.0.2:5000", "http://127.0.0.1:5001"}),
			prependExisting: false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']

[host.'http://127.0.0.2:5000']
capabilities = ['pull', 'resolve']

[host.'http://127.0.0.1:5001']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:            "_default registry mirrors",
			resolveTags:     true,
			registries:      stringListToUrlList(t, []string{}),
			mirrors:         stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			prependExisting: false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_default/hosts.toml": `[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:            "resolve tags disabled",
			resolveTags:     false,
			registries:      stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:         stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			prependExisting: false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull']`,
			},
		},
		{
			name:                "config path directory does not exist",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: false,
			prependExisting:     false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:                "config path directory does exist",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			prependExisting:     false,
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:                "config path directory contains configuration",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": "hello = 'world'",
				"/etc/containerd/certs.d/ghcr.io/hosts.toml":   "foo = 'bar'",
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "hello = 'world'",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:                "config path directory contains backup",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "hello = 'world'",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"/etc/containerd/certs.d/test.txt":                     "test",
				"/etc/containerd/certs.d/foo":                          "bar",
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": "hello = 'world'",
				"/etc/containerd/certs.d/_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:                "prepend to existing configuration",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			prependExisting:     true,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:                "prepend existing disabled",
			resolveTags:         true,
			registries:          stringListToUrlList(t, []string{"https://docker.io", "http://foo.bar:5000"}),
			mirrors:             stringListToUrlList(t, []string{"http://127.0.0.1:5000"}),
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
			},
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/_backup/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"/etc/containerd/certs.d/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']`,
			},
		},
		{
			name:            "with basic authentication",
			resolveTags:     true,
			registries:      stringListToUrlList(t, []string{"http://foo.bar:5000"}),
			mirrors:         stringListToUrlList(t, []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"}),
			prependExisting: false,
			username:        "hello",
			password:        "world",
			expectedFiles: map[string]string{
				"/etc/containerd/certs.d/foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
[host.'http://127.0.0.1:5000'.header]
Authorization = 'Basic aGVsbG86d29ybGQ='

[host.'http://127.0.0.1:5001']
capabilities = ['pull', 'resolve']
[host.'http://127.0.0.1:5001'.header]
Authorization = 'Basic aGVsbG86d29ybGQ='`,
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
			err := AddMirrorConfiguration(t.Context(), fs, registryConfigPath, tt.registries, tt.mirrors, tt.resolveTags, tt.prependExisting, tt.username, tt.password)
			require.NoError(t, err)
			ok, err := afero.DirExists(fs, "/etc/containerd/certs.d/_backup")
			require.NoError(t, err)
			require.True(t, ok)
			seenExpectedFiles := maps.Clone(tt.expectedFiles)
			err = afero.Walk(fs, registryConfigPath, func(path string, fi iofs.FileInfo, _ error) error {
				if fi.IsDir() {
					return nil
				}
				expectedContent, ok := tt.expectedFiles[path]
				require.True(t, ok, path)
				delete(seenExpectedFiles, path)
				b, err := afero.ReadFile(fs, path)
				require.NoError(t, err)
				require.Equal(t, expectedContent, string(b))
				return nil
			})
			require.NoError(t, err)
			require.Empty(t, seenExpectedFiles)
		})
	}
}

func TestMirrorConfigurationInvalidMirrorURL(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	mirrors := stringListToUrlList(t, []string{"http://127.0.0.1:5000"})

	registries := stringListToUrlList(t, []string{"ftp://docker.io"})
	err := AddMirrorConfiguration(t.Context(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false, "", "")
	require.EqualError(t, err, "invalid registry url scheme must be http or https: ftp://docker.io")

	registries = stringListToUrlList(t, []string{"https://docker.io/foo/bar"})
	err = AddMirrorConfiguration(t.Context(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false, "", "")
	require.EqualError(t, err, "invalid registry url path has to be empty: https://docker.io/foo/bar")

	registries = stringListToUrlList(t, []string{"https://docker.io?foo=bar"})
	err = AddMirrorConfiguration(t.Context(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false, "", "")
	require.EqualError(t, err, "invalid registry url query has to be empty: https://docker.io?foo=bar")

	registries = stringListToUrlList(t, []string{"https://foo@docker.io"})
	err = AddMirrorConfiguration(t.Context(), fs, "/etc/containerd/certs.d", registries, mirrors, true, false, "", "")
	require.EqualError(t, err, "invalid registry url user has to be empty: https://foo@docker.io")
}

func TestExistingHosts(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	u, err := url.Parse("https://ghcr.io")
	require.NoError(t, err)

	eh, err := existingHosts(fs, "", *u)
	require.NoError(t, err)
	require.Empty(t, eh)

	tomlHosts := `server = "https://registry-1.docker.io"
[host."https://mirror.registry"]
  capabilities = ["pull"]
  ca = "/etc/certs/mirror.pem"
  skip_verify = false
  [host."https://mirror.registry".header]
    x-custom-2 = ["value1", "value2"]

[host]

[host."https://mirror-bak.registry/us"]
  capabilities = ["pull"]
  skip_verify = true

[host."http://mirror.registry"]
  capabilities = ["pull"]

[host."https://test-3.registry"]
  client = ["/etc/certs/client-1.pem", "/etc/certs/client-2.pem"]

[host."https://test-2.registry".header]
  x-custom-2 = ["foo"]

[host."https://test-1.registry"]
  capabilities = ["pull", "resolve", "push"]
  ca = ["/etc/certs/test-1-ca.pem", "/etc/certs/special.pem"]
  client = [["/etc/certs/client.cert", "/etc/certs/client.key"],["/etc/certs/client.pem", ""]]

[host."https://test-2.registry"]
  client = "/etc/certs/client.pem"

[host."https://non-compliant-mirror.registry/v2/upstream"]
  capabilities = ["pull"]
  override_path = true`

	err = afero.WriteFile(fs, filepath.Join(backupDir, u.Host, "hosts.toml"), []byte(tomlHosts), 0o644)
	require.NoError(t, err)
	eh, err = existingHosts(fs, "", *u)
	require.NoError(t, err)
	expected := `[host.'https://mirror.registry']
ca = '/etc/certs/mirror.pem'
capabilities = ['pull']
skip_verify = false

[host.'https://mirror.registry'.header]
x-custom-2 = ['value1', 'value2']

[host.'https://mirror-bak.registry/us']
capabilities = ['pull']
skip_verify = true

[host.'http://mirror.registry']
capabilities = ['pull']

[host.'https://test-3.registry']
client = ['/etc/certs/client-1.pem', '/etc/certs/client-2.pem']

[host.'https://test-1.registry']
ca = ['/etc/certs/test-1-ca.pem', '/etc/certs/special.pem']
capabilities = ['pull', 'resolve', 'push']
client = [['/etc/certs/client.cert', '/etc/certs/client.key'], ['/etc/certs/client.pem', '']]

[host.'https://test-2.registry']
client = '/etc/certs/client.pem'

[host.'https://test-2.registry'.header]
x-custom-2 = ['foo']

[host.'https://non-compliant-mirror.registry/v2/upstream']
capabilities = ['pull']
override_path = true`
	require.Equal(t, expected, eh)
}

func TestCleanupMirrorConfiguration(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	err := afero.WriteFile(fs, filepath.Join("certs.d", backupDir, "data.txt"), []byte("hello world"), 0o644)
	require.NoError(t, err)
	err = afero.WriteFile(fs, filepath.Join("certs.d", "foo.bin"), []byte("hello world"), 0o644)
	require.NoError(t, err)
	err = fs.MkdirAll("certs.d/docker.io", 0o755)
	require.NoError(t, err)

	for range 2 {
		err = CleanupMirrorConfiguration(t.Context(), fs, "certs.d")
		require.NoError(t, err)
		files, err := afero.ReadDir(fs, "certs.d")
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.Equal(t, "data.txt", files[0].Name())
	}
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
