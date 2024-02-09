package oci

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containers/storage"
	"github.com/containers/storage/pkg/idtools"
	"github.com/containers/storage/pkg/reexec"

	// "github.com/containers/storage/pkg/reexec"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func localCrio(t *testing.T, imgs []map[string]string, blobs map[digest.Digest][]byte) *Crio {
	t.Helper()

	reexec.Init()

	crioDir := t.TempDir()
	store, err := storage.GetStore(storage.StoreOptions{
		RunRoot:         filepath.Join(crioDir, "run"),
		GraphRoot:       filepath.Join(crioDir, "root"),
		GraphDriverName: "vfs",
		// GraphDriverOptions: []string{"vfs.ignore_chown_errors=true"},
		UIDMap: []idtools.IDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GIDMap: []idtools.IDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		//nolint:errcheck // ignore
		store.Shutdown(true)
	})

	// // l, err := store.CreateLayer("foo", "", nil, "", true, nil)
	// // require.NoError(t, err)
	// // fmt.Printf("%#v\n", l)
	// l, size, err := store.PutLayer("foo", "", nil, "", true, nil, strings.NewReader("Hello World"))
	// require.NoError(t, err)
	// fmt.Println(size)
	// fmt.Printf("%#v\n", l)

	// CRIO handles images slightly differently comapred to Containerd.
	// Each architecture is it's own image in the metadata store.
	// While the index manifest is pulled, it is the architecture specific manifest which is the image digest.
	// The ID of the images will be the digest of the config manifest with the hash type prefix removed.
	for _, img := range imgs {
		dgst := digest.Digest(img["digest"])
		indexData := blobs[dgst]
		imageOptions := storage.ImageOptions{
			Digest:  dgst,
			BigData: []storage.ImageBigDataOption{},
		}
		var idx ocispec.Index
		err := json.Unmarshal(indexData, &idx)
		require.NoError(t, err)
		for _, manifest := range idx.Manifests {
			names := []string{}
			if manifest.Platform.OS == "linux" && manifest.Platform.Architecture == "amd64" {
				names = append(names, img["name"])
			}
			manifestData, ok := blobs[manifest.Digest]
			if !ok {
				continue
			}
			var mft ocispec.Manifest
			err := json.Unmarshal(manifestData, &mft)
			require.NoError(t, err)

			// previousLayerID := ""
			for _, layer := range mft.Layers {
				// layerOpts := storage.LayerOptions{
				// 	OriginalDigest: layer.Digest,
				// 	OriginalSize:   &layer.Size,
				// 	BigData: []storage.LayerBigDataOption{
				// 		{
				// 			// Key:  "foo",
				// 			Data: bytes.NewReader(blobs[layer.Digest]),
				// 		},
				// 	},
				// }

				layerID := strings.TrimPrefix(layer.Digest.String(), "sha256:")
				_, err := store.Layer(layerID)
				if err == nil {
					continue
				}
				l, size, err := store.PutLayer(layerID, "", nil, "", true, nil, bytes.NewReader(blobs[layer.Digest]))
				require.NoError(t, err)
				fmt.Println(size)
				fmt.Printf("%#v\n", l)

				// l, err := store.CreateLayer(layerID, previousLayerID, nil, "", false, nil)
				// require.NoError(t, err)

				// err = store.SetLayerBigData(l.ID, "key", bytes.NewReader(blobs[layer.Digest]))
				// require.NoError(t, err)
				// fmt.Printf("layer: %#v\n\n", l)
				// previousLayerID = l.ID

				// fmt.Println("Getting layer")
				// fmt.Println(store.LayersByCompressedDigest(layer.Digest))
			}

			layers, err := store.Layers()
			require.NoError(t, err)
			// for _, layer := range layers {
			// fmt.Printf("%#v\n\n", layer)
			// }

			imageID := strings.TrimPrefix(mft.Config.Digest.String(), "sha256:")
			_, err = store.Image(imageID)
			if err == nil {
				store.AddNames(imageID, names)
				continue
			}
			require.True(t, errors.Is(err, storage.ErrImageUnknown))

			imageOptions.BigData = append(imageOptions.BigData, storage.ImageBigDataOption{
				Key:    mft.Config.Digest.String(),
				Digest: mft.Config.Digest,
				Data:   blobs[mft.Config.Digest],
			})
			imageOptions.BigData = append(imageOptions.BigData, storage.ImageBigDataOption{
				Key:    fmt.Sprintf("%s-%s", storage.ImageDigestManifestBigDataNamePrefix, manifest.Digest),
				Digest: manifest.Digest,
				Data:   blobs[manifest.Digest],
			})
			imageOptions.BigData = append(imageOptions.BigData, storage.ImageBigDataOption{
				Key:    storage.ImageDigestBigDataKey,
				Digest: manifest.Digest,
				Data:   blobs[manifest.Digest],
			})
			imageOptions.BigData = append(imageOptions.BigData, storage.ImageBigDataOption{
				Key:    fmt.Sprintf("%s-%s", storage.ImageDigestManifestBigDataNamePrefix, dgst),
				Digest: dgst,
				Data:   blobs[dgst],
			})
			_, err = store.CreateImage(imageID, names, layers[0].ID, "", &imageOptions)
			require.NoError(t, err)
		}

	}
	return &Crio{
		store: store,
	}
}
