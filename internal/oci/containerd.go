package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/containerd/containerd"
	apievents "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/events"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/xenitab/pkg/channels"
)

type Containerd struct {
	client      *containerd.Client
	listFilter  string
	eventFilter string
}

func NewContainerd(sock, namespace string, registries []url.URL, imageFilter string) (*Containerd, error) {
	client, err := containerd.New(sock, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("could not create containerd client: %w", err)
	}
	listFilter, eventFilter := createFilters(registries, imageFilter)
	return &Containerd{
		client:      client,
		listFilter:  listFilter,
		eventFilter: eventFilter,
	}, err
}

func (c *Containerd) Subscribe(ctx context.Context) (<-chan Image, <-chan error) {
	imgCh := make(chan Image)
	errCh := make(chan error)
	envelopeCh, cErrCh := c.client.EventService().Subscribe(ctx, c.eventFilter)
	go func() {
		for envelope := range envelopeCh {
			name, err := getEventImageName(envelope)
			if err != nil {
				errCh <- err
				return
			}
			cImg, err := c.client.GetImage(ctx, name)
			if err != nil {
				errCh <- err
				return
			}
			img, err := Parse(cImg.Name(), cImg.Target().Digest)
			if err != nil {
				errCh <- err
				return
			}
			imgCh <- img
		}
	}()
	return imgCh, channels.Merge(errCh, cErrCh)
}

func (c *Containerd) ListImages(ctx context.Context) ([]Image, error) {
	cImgs, err := c.client.ListImages(ctx, c.listFilter)
	if err != nil {
		return nil, err
	}
	imgs := []Image{}
	for _, cImg := range cImgs {
		img, err := Parse(cImg.Name(), cImg.Target().Digest)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

func (c *Containerd) GetImageDigests(ctx context.Context, img Image) ([]string, error) {
	keys := []string{}
	platform := platforms.Default()
	err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		b, err := content.ReadBlob(ctx, c.client.ContentStore(), desc)
		if err != nil {
			return nil, err
		}
		var ud UnknownDocument
		if err := json.Unmarshal(b, &ud); err != nil {
			return nil, err
		}

		switch ud.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			var idx ocispec.Index
			if err := json.Unmarshal(b, &idx); err != nil {
				return nil, err
			}
			for _, manifest := range idx.Manifests {
				if !platform.Match(*manifest.Platform) {
					continue
				}
				keys = append(keys, manifest.Digest.String())
				return []ocispec.Descriptor{manifest}, nil
			}
			return nil, fmt.Errorf("could not find platform architecture in manifest: %v", desc.Digest)
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			var manifest ocispec.Manifest
			if err := json.Unmarshal(b, &manifest); err != nil {
				return nil, err
			}
			keys = append(keys, manifest.Config.Digest.String())
			for _, layer := range manifest.Layers {
				keys = append(keys, layer.Digest.String())
			}
			// TODO: In the images.Manifest implementation there is a platform check that I do not understand
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected media type %v for digest: %v", ud.MediaType, desc.Digest)
	}), ocispec.Descriptor{Digest: img.Digest})
	if err != nil {
		return nil, fmt.Errorf("failed to walk image manifests: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no image digests found")
	}
	keys = append(keys, img.Digest.String())
	return keys, nil
}

func (c *Containerd) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	cImg, err := c.client.GetImage(ctx, ref)
	if err != nil {
		return "", err
	}
	return cImg.Target().Digest, nil
}

func (c *Containerd) GetSize(ctx context.Context, dgst digest.Digest) (int64, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (c *Containerd) GetBlob(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	b, err := content.ReadBlob(ctx, c.client.ContentStore(), ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return nil, "", err
	}
	var ud UnknownDocument
	if err := json.Unmarshal(b, &ud); err != nil {
		return nil, "", err
	}
	if ud.MediaType == "" {
		return nil, "", fmt.Errorf("blob manifest cannot be empty")
	}
	return b, ud.MediaType, nil
}

func (c *Containerd) WriteBlob(ctx context.Context, dst io.Writer, dgst digest.Digest) error {
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return err
	}
	defer ra.Close()
	_, err = io.Copy(dst, content.NewReader(ra))
	if err != nil {
		return err
	}
	return nil
}

func getEventImageName(e *events.Envelope) (string, error) {
	switch e.Topic {
	case "/images/create":
		img := apievents.ImageCreate{}
		err := img.Unmarshal(e.Event.Value)
		if err != nil {
			return "", err
		}
		return img.Name, nil
	case "/images/update":
		img := apievents.ImageUpdate{}
		err := img.Unmarshal(e.Event.Value)
		if err != nil {
			return "", err
		}
		return img.Name, nil
	default:
		return "", fmt.Errorf("unknown topic: %s", e.Topic)
	}
}

func createFilters(registries []url.URL, imageFilter string) (string, string) {
	registryHosts := []string{}
	for _, registry := range registries {
		registryHosts = append(registryHosts, registry.Host)
	}
	if imageFilter != "" {
		imageFilter = "|" + imageFilter
	}
	listFilter := fmt.Sprintf(`name~="%s%s"`, strings.Join(registryHosts, "|"), imageFilter)
	eventFilter := fmt.Sprintf(`topic~="/images/create|/images/update",event.name~="%s%s"`, strings.Join(registryHosts, "|"), imageFilter)
	return listFilter, eventFilter
}
