package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"testing"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestOCIClient(t *testing.T) {
	b, err := os.ReadFile("./testdata/images.json")
	require.NoError(t, err)
	imgs := []map[string]string{}
	err = json.Unmarshal(b, &imgs)
	require.NoError(t, err)
	blobs := map[digest.Digest][]byte{}
	fileItems, err := os.ReadDir("./testdata/blobs")
	require.NoError(t, err)
	for _, item := range fileItems {
		if item.IsDir() {
			continue
		}
		dgst, err := digest.Parse(item.Name())
		require.NoError(t, err)
		b, err := os.ReadFile(path.Join("./testdata/blobs", item.Name()))
		require.NoError(t, err)
		blobs[dgst] = b
	}

	contentStore, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	boltDB, err := bolt.Open(path.Join(t.TempDir(), "bolt.db"), 0644, nil)
	require.NoError(t, err)
	db := metadata.NewDB(boltDB, contentStore, nil)
	imageStore := metadata.NewImageStore(db)
	ctx := namespaces.WithNamespace(context.TODO(), "k8s.io")
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
	containerd := &Containerd{
		client: containerdClient,
	}

	for _, ociClient := range []Client{containerd} {
		t.Run(ociClient.Name(), func(t *testing.T) {
			imgs, err := ociClient.ListImages(ctx)
			require.NoError(t, err)
			require.Len(t, imgs, 5)
			for _, img := range imgs {
				_, err := ociClient.Resolve(ctx, img.Name)
				require.NoError(t, err)
			}

			noPlatformName := "example.com/org/no-platform:test"
			dgst, err := ociClient.Resolve(ctx, noPlatformName)
			require.NoError(t, err)
			img := Image{
				Name:   noPlatformName,
				Digest: dgst,
			}
			_, err = ociClient.AllIdentifiers(ctx, img)
			require.EqualError(t, err, "failed to walk image manifests: could not find any platforms with local content in manifest list: sha256:addc990c58744bdf96364fe89bd4aab38b1e824d51c688edb36c75247cd45fa9")

			contentTests := []struct {
				mediaType string
				dgst      digest.Digest
				size      int64
			}{
				{
					mediaType: ocispec.MediaTypeImageIndex,
					dgst:      digest.Digest("sha256:9430beb291fa7b96997711fc486bc46133c719631aefdbeebe58dd3489217bfe"),
					size:      374,
				},
				{
					mediaType: ocispec.MediaTypeImageManifest,
					dgst:      digest.Digest("sha256:aec8273a5e5aca369fcaa8cecef7bf6c7959d482f5c8cfa2236a6a16e46bbdcf"),
					size:      476,
				},
				{
					mediaType: ocispec.MediaTypeImageConfig,
					dgst:      digest.Digest("sha256:68b8a989a3e08ddbdb3a0077d35c0d0e59c9ecf23d0634584def8bdbb7d6824f"),
					size:      529,
				},
				{
					mediaType: ocispec.MediaTypeImageLayer,
					dgst:      digest.Digest("sha256:3caa2469de2a23cbcc209dd0b9d01cd78ff9a0f88741655991d36baede5b0996"),
					size:      118,
				},
			}
			for _, tt := range contentTests {
				t.Run(tt.mediaType, func(t *testing.T) {
					size, err := ociClient.Size(ctx, tt.dgst)
					require.NoError(t, err)
					require.Equal(t, tt.size, size)
					if tt.mediaType != ocispec.MediaTypeImageLayer {
						b, mediaType, err := ociClient.GetManifest(ctx, tt.dgst)
						require.NoError(t, err)
						require.Equal(t, tt.mediaType, mediaType)
						require.Equal(t, blobs[tt.dgst], b)
					} else {
						var buf bytes.Buffer
						bufferSize := 32768
						err = ociClient.CopyLayer(ctx, tt.dgst, &buf, bufferSize)
						require.NoError(t, err)
						require.Equal(t, blobs[tt.dgst], buf.Bytes())
					}
				})
			}

			identifiersTests := []struct {
				imageName    string
				imageDigest  string
				expectedKeys []string
			}{
				{
					imageName:   "ghcr.io/xenitab/spegel:v0.0.8-with-media-type",
					imageDigest: "sha256:9506c8e7a2d0a098d43cadfd7ecdc3c91697e8188d3a1245943b669f717747b4",
					expectedKeys: []string{
						"sha256:9506c8e7a2d0a098d43cadfd7ecdc3c91697e8188d3a1245943b669f717747b4",
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
					imageName:   "ghcr.io/xenitab/spegel:v0.0.8-without-media-type",
					imageDigest: "sha256:d8df04365d06181f037251de953aca85cc16457581a8fc168f4957c978e1008b",
					expectedKeys: []string{
						"sha256:d8df04365d06181f037251de953aca85cc16457581a8fc168f4957c978e1008b",
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
			}
			for _, tt := range identifiersTests {
				t.Run(tt.imageName, func(t *testing.T) {
					img, err := Parse(tt.imageName, digest.Digest(tt.imageDigest))
					require.NoError(t, err)
					keys, err := ociClient.AllIdentifiers(ctx, img)
					require.NoError(t, err)
					require.Equal(t, tt.expectedKeys, keys)
				})
			}
		})
	}
}
