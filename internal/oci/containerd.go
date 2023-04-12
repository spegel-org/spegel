package oci

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/containerd"
	apievents "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/events"
	"github.com/containerd/containerd/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/valyala/fastjson"
)

type EventTopic string

const (
	EventTopicCreate = "/images/create"
	EventTopicUpdate = "/images/update"
	EventTopicDelete = "/images/delete"
)

type Containerd struct {
	client *containerd.Client
}

func (c *Containerd) ResolveTag(ctx context.Context, tag string) (digest.Digest, error) {
	image, err := c.client.ImageService().Get(ctx, tag)
	if err != nil {
		return "", err
	}
	return image.Target.Digest, nil
}

func (c *Containerd) GetContent(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	b, err := content.ReadBlob(ctx, c.client.ContentStore(), ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return nil, "", err
	}
	mediaType := fastjson.GetString(b, "mediaType")
	if mediaType == "" {
		return nil, "", fmt.Errorf("could not find media type in manifest %s", dgst)
	}
	return b, mediaType, nil
}

func (c *Containerd) GetSize(ctx context.Context, dgst digest.Digest) (int64, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (c *Containerd) Copy(ctx context.Context, dgst digest.Digest, cw io.Writer) error {
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return err
	}
	defer ra.Close()
	re := content.NewReader(ra)
	_, err = io.Copy(cw, re)
	if err != nil {
		return err
	}
	return nil
}

func (c *Containerd) ImageDigests(ctx context.Context, dgst digest.Digest) ([]string, error) {
	manifest, err := images.Manifest(ctx, c.client.ContentStore(), ocispec.Descriptor{}, nil)
	if err != nil {
		return nil, err
	}

	// Add image digest, config and image layers
	keys := []string{}
	keys = append(keys, dgst.String())
	keys = append(keys, manifest.Config.Digest.String())
	for _, layer := range manifest.Layers {
		keys = append(keys, layer.Digest.String())
	}

	// If manifest is of list or index type it needs to be parsed separatly to add the manifest digest for the specific architecture.
	// This is because when the images manifest is fetched through containerd the plaform specific manifest is immediatly returned.
	if img.Metadata().Target.MediaType == images.MediaTypeDockerSchema2ManifestList || img.Metadata().Target.MediaType == ocispec.MediaTypeImageIndex {
		b, err := content.ReadBlob(ctx, c.client.ContentStore(), img.Target())
		if err != nil {
			return nil, err
		}
		var idx ocispec.Index
		if err := json.Unmarshal(b, &idx); err != nil {
			return nil, err
		}
		for _, manifest := range idx.Manifests {
			if !img.Platform().Match(*manifest.Platform) {
				continue
			}
			keys = append(keys, manifest.Digest.String())
			break
		}
	}

	return keys, nil
}

func getEventImageName(e *events.Envelope) (string, error) {
	switch e.Topic {
	case EventTopicCreate:
		img := apievents.ImageCreate{}
		err := img.Unmarshal(e.Event.Value)
		if err != nil {
			return "", err
		}
		return img.Name, nil
	case EventTopicUpdate:
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
