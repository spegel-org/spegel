package oci

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"testing"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

type readerAt struct {
	bytes.Reader
}

func (r *readerAt) Close() error {
	return nil
}

type mockContentStore struct {
	data map[string]string
}

func (*mockContentStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	panic("not implemented")
}

func (*mockContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	panic("not implemented")
}

func (*mockContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	panic("not implemented")
}

func (m *mockContentStore) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	s, ok := m.data[desc.Digest.String()]
	if !ok {
		return nil, fmt.Errorf("digest not found: %s", desc.Digest.String())
	}
	return &readerAt{*bytes.NewReader([]byte(s))}, nil
}

func (*mockContentStore) Status(ctx context.Context, ref string) (content.Status, error) {
	panic("not implemented")
}

func (*mockContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	panic("not implemented")
}

func (*mockContentStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	panic("not implemented")
}

func (*mockContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	panic("not implemented")
}

func (*mockContentStore) Abort(ctx context.Context, ref string) error {
	panic("not implemented")
}

func TestGetImageDigests(t *testing.T) {
	tests := []struct {
		name         string
		platformStr  string
		imgDigest    string
		expectedKeys []string
	}{
		{
			platformStr: "linux/amd64",
			imgDigest:   "sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
			expectedKeys: []string{
				"sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
				"sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355",
				"sha256:d715ba0d85ee7d37da627d0679652680ed2cb23dde6120f25143a0b8079ee47e",
				"sha256:a7ca0d9ba68fdce7e15bc0952d3e898e970548ca24d57698725836c039086639",
				"sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58",
				"sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db",
				"sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265",
				"sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0",
				"sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c",
				"sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f",
				"sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c",
				"sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a",
				"sha256:76f3a495ffdc00c612747ba0c59fc56d0a2610d2785e80e9edddbf214c2709ef",
				"sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1",
			},
		},
		{
			platformStr: "linux/arm64",
			imgDigest:   "sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
			expectedKeys: []string{
				"sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
				"sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53",
				"sha256:c73129c9fb699b620aac2df472196ed41797fd0f5a90e1942bfbf19849c4a1c9",
				"sha256:0b41f743fd4d78cb50ba86dd3b951b51458744109e1f5063a76bc5a792c3d8e7",
				"sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58",
				"sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db",
				"sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265",
				"sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0",
				"sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c",
				"sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f",
				"sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c",
				"sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a",
				"sha256:0dc769edeab7d9f622b9703579f6c89298a4cf45a84af1908e26fffca55341e1",
				"sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1",
			},
		},
		{
			platformStr: "linux/arm",
			imgDigest:   "sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
			expectedKeys: []string{
				"sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a",
				"sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b",
				"sha256:1079836371d57a148a0afa5abfe00bd91825c869fcc6574a418f4371d53cab4c",
				"sha256:b437b30b8b4cc4e02865517b5ca9b66501752012a028e605da1c98beb0ed9f50",
				"sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58",
				"sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db",
				"sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265",
				"sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0",
				"sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c",
				"sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f",
				"sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c",
				"sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a",
				"sha256:01d28554416aa05390e2827a653a1289a2a549e46cc78d65915a75377c6008ba",
				"sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1",
			},
		},
	}

	cs := &mockContentStore{
		data: map[string]string{
			// Index
			"sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a": `{ "mediaType": "application/vnd.oci.image.index.v1+json", "schemaVersion": 2, "manifests": [ { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355", "size": 2372, "platform": { "architecture": "amd64", "os": "linux" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b", "size": 2372, "platform": { "architecture": "arm", "os": "linux", "variant": "v7" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53", "size": 2372, "platform": { "architecture": "arm64", "os": "linux" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:73af5483f4d2d636275dcef14d5443ff96d7347a0720ca5a73a32c73855c4aac", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:36e11bf470af256febbdfad9d803e60b7290b0268218952991b392be9e8153bd", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:42d1c43f2285e8e3d39f80b8eed8e4c5c28b8011c942b5413ecc6a0050600609", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } } ] }`,
			// AMD64
			"sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355": `{ "mediaType": "application/vnd.oci.image.manifest.v1+json", "schemaVersion": 2, "config": { "mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:d715ba0d85ee7d37da627d0679652680ed2cb23dde6120f25143a0b8079ee47e", "size": 2842 }, "layers": [ { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:a7ca0d9ba68fdce7e15bc0952d3e898e970548ca24d57698725836c039086639", "size": 103732 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58", "size": 21202 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db", "size": 716491 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265", "size": 317 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0", "size": 198 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c", "size": 113 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f", "size": 385 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c", "size": 355 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a", "size": 130562 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:76f3a495ffdc00c612747ba0c59fc56d0a2610d2785e80e9edddbf214c2709ef", "size": 36529876 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1", "size": 32 } ] }`,
			// ARM64
			"sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53": `{ "mediaType": "application/vnd.oci.image.manifest.v1+json", "schemaVersion": 2, "config": { "mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:c73129c9fb699b620aac2df472196ed41797fd0f5a90e1942bfbf19849c4a1c9", "size": 2842 }, "layers": [ { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:0b41f743fd4d78cb50ba86dd3b951b51458744109e1f5063a76bc5a792c3d8e7", "size": 103732 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58", "size": 21202 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db", "size": 716491 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265", "size": 317 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0", "size": 198 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c", "size": 113 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f", "size": 385 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c", "size": 355 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a", "size": 130562 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:0dc769edeab7d9f622b9703579f6c89298a4cf45a84af1908e26fffca55341e1", "size": 34168923 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1", "size": 32 } ] }`,
			// ARM
			"sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b": `{ "mediaType": "application/vnd.oci.image.manifest.v1+json", "schemaVersion": 2, "config": { "mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:1079836371d57a148a0afa5abfe00bd91825c869fcc6574a418f4371d53cab4c", "size": 2855 }, "layers": [ { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:b437b30b8b4cc4e02865517b5ca9b66501752012a028e605da1c98beb0ed9f50", "size": 103732 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fe5ca62666f04366c8e7f605aa82997d71320183e99962fa76b3209fdfbb8b58", "size": 21202 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:b02a7525f878e61fc1ef8a7405a2cc17f866e8de222c1c98fd6681aff6e509db", "size": 716491 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:fcb6f6d2c9986d9cd6a2ea3cc2936e5fc613e09f1af9042329011e43057f3265", "size": 317 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:e8c73c638ae9ec5ad70c49df7e484040d889cca6b4a9af056579c3d058ea93f0", "size": 198 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:1e3d9b7d145208fa8fa3ee1c9612d0adaac7255f1bbc9ddea7e461e0b317805c", "size": 113 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4aa0ea1413d37a58615488592a0b827ea4b2e48fa5a77cf707d0e35f025e613f", "size": 385 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:7c881f9ab25e0d86562a123b5fb56aebf8aa0ddd7d48ef602faf8d1e7cf43d8c", "size": 355 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:5627a970d25e752d971a501ec7e35d0d6fdcd4a3ce9e958715a686853024794a", "size": 130562 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:01d28554416aa05390e2827a653a1289a2a549e46cc78d65915a75377c6008ba", "size": 34318536 }, { "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1", "size": 32 } ] }`,
		},
	}
	client, err := containerd.New("", containerd.WithServices(containerd.WithContentStore(cs)))
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.platformStr, func(t *testing.T) {
			c := Containerd{
				client:   client,
				platform: platforms.Only(platforms.MustParse(tt.platformStr)),
			}
			img := Image{
				Digest: digest.Digest(tt.imgDigest),
			}
			keys, err := c.GetImageDigests(context.TODO(), img)
			require.NoError(t, err)
			require.Equal(t, tt.expectedKeys, keys)
		})
	}
}

func TestGetImageDigestsNoPlatform(t *testing.T) {
	cs := &mockContentStore{
		data: map[string]string{
			// Index
			"sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a": `{ "mediaType": "application/vnd.oci.image.index.v1+json", "schemaVersion": 2, "manifests": [ { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355", "size": 2372, "platform": { "architecture": "amd64", "os": "linux" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b", "size": 2372, "platform": { "architecture": "arm", "os": "linux", "variant": "v7" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53", "size": 2372, "platform": { "architecture": "arm64", "os": "linux" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:73af5483f4d2d636275dcef14d5443ff96d7347a0720ca5a73a32c73855c4aac", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:44cb2cf712c060f69df7310e99339c1eb51a085446f1bb6d44469acff35b4355", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:36e11bf470af256febbdfad9d803e60b7290b0268218952991b392be9e8153bd", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:0ad7c556c55464fa44d4c41e5236715e015b0266daced62140fb5c6b983c946b", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } }, { "mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:42d1c43f2285e8e3d39f80b8eed8e4c5c28b8011c942b5413ecc6a0050600609", "size": 566, "annotations": { "vnd.docker.reference.digest": "sha256:dce623533c59af554b85f859e91fc1cbb7f574e873c82f36b9ea05a09feb0b53", "vnd.docker.reference.type": "attestation-manifest" }, "platform": { "architecture": "unknown", "os": "unknown" } } ] }`,
		},
	}
	client, err := containerd.New("", containerd.WithServices(containerd.WithContentStore(cs)))
	require.NoError(t, err)
	c := Containerd{
		client:   client,
		platform: platforms.Only(platforms.MustParse("darwin/arm64")),
	}
	img := Image{
		Digest: digest.Digest("sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a"),
	}
	_, err = c.GetImageDigests(context.TODO(), img)
	require.EqualError(t, err, "failed to walk image manifests: could not find platform architecture in manifest: sha256:e80e36564e9617f684eb5972bf86dc9e9e761216e0d40ff78ca07741ec70725a")
}

func TestCreateFilter(t *testing.T) {
	tests := []struct {
		name                string
		registries          []string
		expectedListFilter  string
		expectedEventFilter string
	}{
		{
			name:                "only registries",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="docker.io|gcr.io"`,
			expectedEventFilter: `topic~="/images/create|/images/update",event.name~="docker.io|gcr.io"`,
		},
		{
			name:                "additional image filtes",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="docker.io|gcr.io"`,
			expectedEventFilter: `topic~="/images/create|/images/update",event.name~="docker.io|gcr.io"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listFilter, eventFilter := createFilters(stringListToUrlList(t, tt.registries))
			require.Equal(t, listFilter, tt.expectedListFilter)
			require.Equal(t, eventFilter, tt.expectedEventFilter)
		})
	}
}

func TestHostFileContent(t *testing.T) {
	tests := []struct {
		name            string
		registry        string
		mirrors         []string
		expectedContent string
	}{
		{
			name:     "example registry",
			registry: "https://example.com",
			mirrors:  []string{"http://127.0.0.1:5000"},
			expectedContent: `server = "https://example.com"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]`,
		},
		{
			name:     "multiple mirrors",
			registry: "https://example.com",
			mirrors:  []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"},
			expectedContent: `server = "https://example.com"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]

[host."http://127.0.0.1:5001"]
  capabilities = ["pull", "resolve"]`,
		},
		{
			name:     "docker override",
			registry: "https://docker.io",
			mirrors:  []string{"http://127.0.0.1:5000"},
			expectedContent: `server = "https://registry-1.docker.io"

[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registryURL, err := url.Parse(tt.registry)
			require.NoError(t, err)
			mirrorUrls := stringListToUrlList(t, tt.mirrors)
			content := hostsFileContent(*registryURL, mirrorUrls)
			require.Equal(t, tt.expectedContent, content)
		})
	}
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
