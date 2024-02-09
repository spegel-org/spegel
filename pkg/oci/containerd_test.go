package oci

import (
	"context"
	"fmt"
	iofs "io/fs"
	"net/url"
	"path"
	"testing"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/metadata"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func TestVerifyStatusResponse(t *testing.T) {
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
			resp := &runtimeapi.StatusResponse{
				Info: map[string]string{
					"config": fmt.Sprintf(`{"registry": {"configPath": "%s"}, "containerd": {"runtimes":{"discardUnpackedLayers": %v}}}`, tt.configPath, tt.discardUnpackedLayers),
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

func TestCreateFilter(t *testing.T) {
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
			listFilter, eventFilter := createFilters(stringListToUrlList(t, tt.registries))
			require.Equal(t, tt.expectedListFilter, listFilter)
			require.Equal(t, tt.expectedEventFilter, eventFilter)
		})
	}
}

func TestMirrorConfiguration(t *testing.T) {
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
			fs := afero.NewMemMapFs()
			if tt.createConfigPathDir {
				err := fs.Mkdir(registryConfigPath, 0755)
				require.NoError(t, err)
			}
			for k, v := range tt.existingFiles {
				err := afero.WriteFile(fs, k, []byte(v), 0644)
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

func localContainerd(t *testing.T, ctx context.Context, imgs []map[string]string, blobs map[digest.Digest][]byte) *Containerd {
	t.Helper()

	contentStore, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	boltDB, err := bolt.Open(path.Join(t.TempDir(), "bolt.db"), 0644, nil)
	require.NoError(t, err)
	db := metadata.NewDB(boltDB, contentStore, nil)
	imageStore := metadata.NewImageStore(db)
	for _, img := range imgs {
		dgst, err := digest.Parse(img["digest"])
		require.NoError(t, err)
		cImg := images.Image{
			Name: img["name"],
			Target: ocispec.Descriptor{
				MediaType: img["mediaType"],
				Digest:    dgst,
				Size:      int64(len(blobs[dgst])),
			},
		}
		_, err = imageStore.Create(ctx, cImg)
		require.NoError(t, err)
	}
	for k, v := range blobs {
		writer, err := contentStore.Writer(ctx, content.WithRef(k.String()))
		require.NoError(t, err)
		_, err = writer.Write(v)
		require.NoError(t, err)
		err = writer.Commit(ctx, int64(len(v)), k)
		require.NoError(t, err)
		writer.Close()
	}
	containerdClient, err := containerd.New("", containerd.WithServices(containerd.WithImageStore(imageStore), containerd.WithContentStore(contentStore)))
	require.NoError(t, err)
	return &Containerd{
		client: containerdClient,
	}
}
