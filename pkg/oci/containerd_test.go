package oci

import (
	iofs "io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

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
			mirroredRegistries: []string{wildcardRegistries[0]},
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
				"docker.io/hello.txt":       "Hello World",
				"docker.io/nested/cert.tls": "Foo Bar",
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
				"_backup/docker.io/hello.txt":       "Hello World",
				"_backup/docker.io/nested/cert.tls": "Foo Bar",
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
				"docker.io/hello.txt":       "Hello World",
				"docker.io/nested/cert.tls": "Foo Bar",
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
				require.True(t, ok, relPath)
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
