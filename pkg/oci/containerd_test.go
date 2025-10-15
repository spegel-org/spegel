package oci

import (
	"fmt"
	iofs "io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
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

	configPath := t.TempDir()
	err := backupConfig(log, configPath)
	require.NoError(t, err)
	ok, err := dirExists(filepath.Join(configPath, "_backup"))
	require.NoError(t, err)
	require.True(t, ok)
	files, err := os.ReadDir(filepath.Join(configPath, "_backup"))
	require.NoError(t, err)
	require.Empty(t, files)

	configPath = t.TempDir()
	err = os.WriteFile(filepath.Join(configPath, "test.txt"), []byte("Hello World"), 0o644)
	require.NoError(t, err)
	err = backupConfig(log, configPath)
	require.NoError(t, err)
	ok, err = dirExists(filepath.Join(configPath, "_backup"))
	require.NoError(t, err)
	require.True(t, ok)
	files, err = os.ReadDir(filepath.Join(configPath, "_backup"))
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestContentLabelsToReferences(t *testing.T) {
	t.Parallel()

	dgst := digest.Digest("foo")
	tests := []struct {
		name     string
		labels   map[string]string
		expected []Reference
	}{
		{
			name: "one matching",
			labels: map[string]string{
				"containerd.io/distribution.source.docker.io": "library/alpine",
			},
			expected: []Reference{
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
			expected: []Reference{
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
			require.ElementsMatch(t, tt.expected, refs)
		})
	}

	_, err := contentLabelsToReferences(map[string]string{}, dgst)
	require.EqualError(t, err, "no distribution source labels found for foo")
}

func TestFeaturesForVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version          string
		expectedString   string
		expectedFeatures []Feature
	}{
		{
			version:          "v2.0.2",
			expectedFeatures: []Feature{},
			expectedString:   "",
		},
		{
			version:          "2.1.0",
			expectedFeatures: []Feature{FeatureContentEvent},
			expectedString:   "ContentEvent",
		},
		{
			version:          "v1.7.27",
			expectedFeatures: []Feature{FeatureConfigCheck},
			expectedString:   "ConfigCheck",
		},
		{
			version:          "1.6.0",
			expectedFeatures: []Feature{FeatureConfigCheck},
			expectedString:   "ConfigCheck",
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

				feats, err := featuresForVersion(tt.version)
				require.NoError(t, err)
				for _, feat := range tt.expectedFeatures {
					ok := feats.Has(feat)
					require.True(t, ok)
				}
				require.Equal(t, tt.expectedString, feats.String())
			})
		}
	}
}

func TestCreateFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		expectedImageFilter   []string
		expectedEventFilter   []string
		expectedContentFilter []string
		registries            []string
	}{
		{
			name:                  "with registry filtering",
			registries:            []string{"https://docker.io", "https://gcr.io"},
			expectedImageFilter:   []string{`name~="^(docker\\.io|gcr\\.io)/"`},
			expectedEventFilter:   []string{`topic~="/images/create|/images/delete",event.name~="^(docker\\.io|gcr\\.io)/"`, `topic~="/content/create"`},
			expectedContentFilter: []string{`labels."containerd.io/distribution.source.docker.io"~="^."`, `labels."containerd.io/distribution.source.gcr.io"~="^."`},
		},
		{
			name:                  "without registry filtering",
			registries:            []string{},
			expectedImageFilter:   []string{`name~="^.+/"`},
			expectedEventFilter:   []string{`topic~="/images/create|/images/delete",event.name~="^.+/"`, `topic~="/content/create"`},
			expectedContentFilter: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			imageFilter, eventFilter, contentFilter := createFilters(stringListToUrlList(t, tt.registries))

			require.Equal(t, tt.expectedImageFilter, imageFilter)
			_, err := filters.ParseAll(imageFilter...)
			require.NoError(t, err)
			require.Equal(t, tt.expectedEventFilter, eventFilter)
			_, err = filters.ParseAll(eventFilter...)
			require.NoError(t, err)
			require.Equal(t, tt.expectedContentFilter, contentFilter)
			_, err = filters.ParseAll(contentFilter...)
			require.NoError(t, err)
		})
	}
}

func TestMirrorConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		existingFiles       map[string]string
		expectedFiles       map[string]string
		name                string
		username            string
		password            string
		mirroredRegistries  []string
		mirrorTargets       []string
		resolveTags         bool
		createConfigPathDir bool
		prependExisting     bool
	}{
		{
			name:               "multiple mirrors targets",
			resolveTags:        true,
			mirroredRegistries: []string{"http://foo.bar:5000"},
			mirrorTargets:      []string{"http://127.0.0.1:5000", "http://127.0.0.2:5000", "http://127.0.0.1:5001"},
			prependExisting:    false,
			expectedFiles: map[string]string{
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'

[host.'http://127.0.0.2:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'

[host.'http://127.0.0.1:5001']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:               "empty mirrored registires defaults to _default",
			resolveTags:        true,
			mirroredRegistries: []string{},
			mirrorTargets:      []string{"http://127.0.0.1:5000"},
			prependExisting:    false,
			expectedFiles: map[string]string{
				"_default/hosts.toml": `[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:               "default is explicitly set",
			resolveTags:        true,
			mirroredRegistries: []string{wildcardRegistryMirrors[0]},
			mirrorTargets:      []string{"http://127.0.0.1:5000"},
			prependExisting:    false,
			expectedFiles: map[string]string{
				"_default/hosts.toml": `[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:               "resolve tags disabled",
			resolveTags:        false,
			mirroredRegistries: []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:      []string{"http://127.0.0.1:5000"},
			prependExisting:    false,
			expectedFiles: map[string]string{
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "config path directory does not exist",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: false,
			prependExisting:     false,
			expectedFiles: map[string]string{
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "config path directory does exist",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: true,
			prependExisting:     false,
			expectedFiles: map[string]string{
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "config path directory contains configuration",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"docker.io/hosts.toml": "hello = 'world'",
				"ghcr.io/hosts.toml":   "foo = 'bar'",
			},
			expectedFiles: map[string]string{
				"_backup/docker.io/hosts.toml": "hello = 'world'",
				"_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "config path directory contains backup",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"_backup/docker.io/hosts.toml": "hello = 'world'",
				"_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"test.txt":                     "test",
				"foo":                          "bar",
			},
			expectedFiles: map[string]string{
				"_backup/docker.io/hosts.toml": "hello = 'world'",
				"_backup/ghcr.io/hosts.toml":   "foo = 'bar'",
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "prepend to existing configuration",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: true,
			prependExisting:     true,
			existingFiles: map[string]string{
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

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
				"_backup/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:                "prepend existing disabled",
			resolveTags:         true,
			mirroredRegistries:  []string{"https://docker.io", "http://foo.bar:5000"},
			mirrorTargets:       []string{"http://127.0.0.1:5000"},
			createConfigPathDir: true,
			prependExisting:     false,
			existingFiles: map[string]string{
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

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
				"_backup/docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://example.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']

[host.'http://example.com:30021']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']
capabilities = ['pull', 'resolve']

[host.'http://bar.com:30020']
capabilities = ['pull', 'resolve']
client = ['/etc/certs/xxx/client.cert', '/etc/certs/xxx/client.key']`,
				"docker.io/hosts.toml": `server = 'https://registry-1.docker.io'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'`,
			},
		},
		{
			name:               "with basic authentication",
			resolveTags:        true,
			mirroredRegistries: []string{"http://foo.bar:5000"},
			mirrorTargets:      []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"},
			prependExisting:    false,
			username:           "hello",
			password:           "world",
			expectedFiles: map[string]string{
				"foo.bar:5000/hosts.toml": `server = 'http://foo.bar:5000'

[host.'http://127.0.0.1:5000']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'
[host.'http://127.0.0.1:5000'.header]
Authorization = 'Basic aGVsbG86d29ybGQ='

[host.'http://127.0.0.1:5001']
capabilities = ['pull', 'resolve']
dial_timeout = '200ms'
[host.'http://127.0.0.1:5001'.header]
Authorization = 'Basic aGVsbG86d29ybGQ='`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registryConfigPath := filepath.Join(t.TempDir(), "etc", "containerd", "certs.d")
			if tt.createConfigPathDir {
				err := os.MkdirAll(registryConfigPath, 0o755)
				require.NoError(t, err)
			}
			for k, v := range tt.existingFiles {
				path := filepath.Join(registryConfigPath, k)
				err := os.MkdirAll(filepath.Dir(path), 0o755)
				require.NoError(t, err)
				err = os.WriteFile(path, []byte(v), 0o644)
				require.NoError(t, err)
			}
			err := AddMirrorConfiguration(t.Context(), registryConfigPath, tt.mirroredRegistries, tt.mirrorTargets, tt.resolveTags, tt.prependExisting, tt.username, tt.password)
			require.NoError(t, err)
			ok, err := dirExists(filepath.Join(registryConfigPath, "_backup"))
			require.NoError(t, err)
			require.True(t, ok)
			seenExpectedFiles := maps.Clone(tt.expectedFiles)
			err = filepath.Walk(registryConfigPath, func(path string, fi iofs.FileInfo, _ error) error {
				if fi.IsDir() {
					return nil
				}
				relPath, err := filepath.Rel(registryConfigPath, path)
				require.NoError(t, err)
				expectedContent, ok := tt.expectedFiles[relPath]
				require.True(t, ok)
				delete(seenExpectedFiles, relPath)
				b, err := os.ReadFile(path)
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

	configPath := filepath.Join(t.TempDir(), "etc", "containerd", "certs.d")
	mirrorTargets := []string{"http://127.0.0.1:5000"}

	mirroredRegistries := []string{"ftp://docker.io"}
	err := AddMirrorConfiguration(t.Context(), configPath, mirroredRegistries, mirrorTargets, true, false, "", "")
	require.EqualError(t, err, "invalid registry url scheme must be http or https: ftp://docker.io")

	mirroredRegistries = []string{"https://docker.io/foo/bar"}
	err = AddMirrorConfiguration(t.Context(), configPath, mirroredRegistries, mirrorTargets, true, false, "", "")
	require.EqualError(t, err, "invalid registry url path has to be empty: https://docker.io/foo/bar")

	mirroredRegistries = []string{"https://docker.io?foo=bar"}
	err = AddMirrorConfiguration(t.Context(), configPath, mirroredRegistries, mirrorTargets, true, false, "", "")
	require.EqualError(t, err, "invalid registry url query has to be empty: https://docker.io?foo=bar")

	mirroredRegistries = []string{"https://foo@docker.io"}
	err = AddMirrorConfiguration(t.Context(), configPath, mirroredRegistries, mirrorTargets, true, false, "", "")
	require.EqualError(t, err, "invalid registry url user has to be empty: https://foo@docker.io")
}

func TestExistingHosts(t *testing.T) {
	t.Parallel()

	configPath := t.TempDir()
	u, err := url.Parse("https://ghcr.io")
	require.NoError(t, err)

	eh, err := existingHosts(configPath, *u)
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
	err = os.MkdirAll(filepath.Join(configPath, backupDir, u.Host), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(configPath, backupDir, u.Host, "hosts.toml"), []byte(tomlHosts), 0o644)
	require.NoError(t, err)
	eh, err = existingHosts(configPath, *u)
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

	configPath := filepath.Join(t.TempDir(), "certs.d")
	err := os.MkdirAll(filepath.Join(configPath, "_backup"), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(configPath, backupDir, "data.txt"), []byte("hello world"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(configPath, "foo.bin"), []byte("hello world"), 0o644)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(configPath, "docker.io"), 0o755)
	require.NoError(t, err)

	for range 2 {
		err = CleanupMirrorConfiguration(t.Context(), configPath)
		require.NoError(t, err)
		files, err := os.ReadDir(configPath)
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
