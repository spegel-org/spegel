package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/containers/storage"
	"github.com/containers/storage/types"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var _ Client = &Crio{}

type Crio struct {
	store storage.Store
}

func NewCrio() (*Crio, error) {
	opts := types.StoreOptions{}
	store, err := storage.GetStore(opts)
	if err != nil {
		return nil, err
	}
	return &Crio{
		store: store,
	}, nil
}

func (c *Crio) Name() string {
	return "crio"
}

func (c *Crio) Verify(ctx context.Context) error {
	return nil
}

func (c *Crio) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error) {
	return nil, nil
}

func (c *Crio) ListImages(ctx context.Context) ([]Image, error) {
	// TODO: Implement registry filtering
	images, err := c.store.Images()
	if err != nil {
		return nil, err
	}
	imgs := []Image{}
	for _, sImg := range images {
		for _, name := range sImg.Names {
			img, err := Parse(name, sImg.Digest)
			if err != nil {
				return nil, err
			}
			imgs = append(imgs, img)
		}
	}
	return imgs, nil
}

func (c *Crio) AllIdentifiers(ctx context.Context, img Image) ([]string, error) {
	cImg, err := c.getImageByDigest(img.Digest)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	keys = append(keys, cImg.Names...)
	keys = append(keys, cImg.Digest.String())
	for _, v := range cImg.BigDataDigests {
		_, _, err := c.GetManifest(ctx, v)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		keys = append(keys, v.String())
	}
	// Error if no manifests could be found
	if len(keys) == 2 {
		return nil, fmt.Errorf("could not find any platforms with local content in manifest list: %v", cImg.Digest)
	}
	layerID := cImg.TopLayer
	for layerID != "" {
		layer, err := c.store.Layer(layerID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, layer.ID)
		layerID = layer.Parent
	}
	return keys, nil
}

func (c *Crio) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	img, err := c.store.Image(ref)
	if err != nil {
		return "", err
	}
	return img.Digest, nil
}

func (c *Crio) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	layers, err := c.store.LayersByCompressedDigest(dgst)
	if err != nil && !errors.Is(err, storage.ErrLayerUnknown) {
		return 0, err
	}
	if len(layers) > 0 {
		return layers[0].CompressedSize, nil
	}
	img, err := c.getImageByDigest(dgst)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s-%s", storage.ImageDigestManifestBigDataNamePrefix, dgst.String())
	if img.ID == strings.TrimPrefix(dgst.String(), "sha256:") {
		key = dgst.String()
	}
	size, ok := img.BigDataSizes[key]
	if !ok {
		return 0, fmt.Errorf("size not found for digest %s", dgst)
	}
	return size, nil
}

func (c *Crio) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	img, err := c.getImageByDigest(dgst)
	if err != nil {
		return nil, "", err
	}
	key := fmt.Sprintf("%s-%s", storage.ImageDigestManifestBigDataNamePrefix, dgst.String())
	isConfig := false
	if img.ID == strings.TrimPrefix(dgst.String(), "sha256:") {
		isConfig = true
		key = dgst.String()
	}
	b, err := c.store.ImageBigData(img.ID, key)
	if err != nil {
		return nil, "", err
	}
	if isConfig {
		return b, ocispec.MediaTypeImageConfig, nil
	}
	var ud UnknownDocument
	if err := json.Unmarshal(b, &ud); err != nil {
		return nil, "", err
	}
	if ud.MediaType != "" {
		return b, ud.MediaType, nil
	}
	return nil, "", fmt.Errorf("could not resolve %s media type", dgst.String())
}

func (c *Crio) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	layers, err := c.store.LayersByCompressedDigest(dgst)
	if err != nil {
		return nil, err
	}
	src, err := c.store.Diff("", layers[0].ID, nil)
	if err != nil {
		return nil, err
	}
	return src, nil
}

func (c *Crio) CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer) error {
	layers, err := c.store.LayersByCompressedDigest(dgst)
	if err != nil {
		return err
	}
	src, err := c.store.Diff("", layers[0].ID, nil)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}
	return nil
}

func (c *Crio) getImageByDigest(dgst digest.Digest) (*storage.Image, error) {
	// TODO: Figure out why config digest does not return an image
	imgs, err := c.store.ImagesByDigest(dgst)
	if err != nil {
		return nil, err
	}
	if len(imgs) > 0 {
		return imgs[0], nil
	}
	id := strings.TrimPrefix(dgst.String(), "sha256:")
	img, err := c.store.Image(id)
	if err != nil {
		return nil, fmt.Errorf("image containing digest %s does not exist: %w", dgst.String(), err)
	}
	return img, nil
}
