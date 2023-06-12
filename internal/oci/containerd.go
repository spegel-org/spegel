package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/containerd/containerd"
	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/afero"
	"github.com/xenitab/pkg/channels"

	"github.com/xenitab/spegel/internal/header"
)

type Containerd struct {
	client      *containerd.Client
	listFilter  string
	eventFilter string
}

func NewContainerd(sock, namespace string, registries []url.URL) (*Containerd, error) {
	client, err := containerd.New(sock, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("could not create containerd client: %w", err)
	}
	listFilter, eventFilter := createFilters(registries)
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
			imageName, err := getEventImage(envelope.Event)
			if err != nil {
				errCh <- err
				return
			}
			cImg, err := c.client.GetImage(ctx, imageName)
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

func getEventImage(e typeurl.Any) (string, error) {
	evt, err := typeurl.UnmarshalAny(e)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshalany: %w", err)
	}
	switch e := evt.(type) {
	case *eventtypes.ImageCreate:
		return e.Name, nil
	case *eventtypes.ImageUpdate:
		return e.Name, nil
	default:
		return "", errors.New("unsupported event")
	}
}

func createFilters(registries []url.URL) (string, string) {
	registryHosts := []string{}
	for _, registry := range registries {
		registryHosts = append(registryHosts, registry.Host)
	}
	listFilter := fmt.Sprintf(`name~="%s"`, strings.Join(registryHosts, "|"))
	eventFilter := fmt.Sprintf(`topic~="/images/create|/images/update",event.name~="%s"`, strings.Join(registryHosts, "|"))
	return listFilter, eventFilter
}

// Refer to containerd registry configuration documentation for mor information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs, mirrorURLs []url.URL) error {
	if err := validate(registryURLs); err != nil {
		return err
	}
	for _, registryURL := range registryURLs {
		content := hostsFileContent(registryURL, mirrorURLs)
		fp := path.Join(configPath, registryURL.Host, "hosts.toml")
		err := fs.MkdirAll(path.Dir(fp), 0755)
		if err != nil {
			return err
		}
		err = afero.WriteFile(fs, fp, []byte(content), 0644)
		if err != nil {
			return err
		}
		logr.FromContextOrDiscard(ctx).Info("added containerd mirror configuration", "registry", registryURL.String(), "path", fp)
	}
	return nil
}

func RemoveMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs []url.URL) error {
	errs := []error{}
	for _, registryURL := range registryURLs {
		dp := path.Join(configPath, registryURL.Host)
		err := fs.RemoveAll(dp)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		logr.FromContextOrDiscard(ctx).Info("removed containerd mirror configuration", "registry", registryURL.String(), "path", dp)
	}
	return errors.Join(errs...)
}

func hostsFileContent(registryURL url.URL, mirrorURLs []url.URL) string {
	server := registryURL.String()
	// Need a special case for Docker Hub as docker.io is just an alias.
	if registryURL.String() == "https://docker.io" {
		server = "https://registry-1.docker.io"
	}
	content := fmt.Sprintf(`server = "%s"`, server)
	for i, mirrorURL := range mirrorURLs {
		content = fmt.Sprintf(`%[1]s

[host."%[3]s"]
  capabilities = ["pull", "resolve"]
[host."%[3]s".header]
  %[4]s = ["%[2]s"]
  %[5]s = ["true"]`, content, registryURL.String(), mirrorURL.String(), header.RegistryHeader, header.MirrorHeader)

		// We assume first mirror registry is local. All others are external.
		if i != 0 {
			content = fmt.Sprintf(`%s
  %s = ["true"]`, content, header.ExternalHeader)
		}
	}
	return content
}

func validate(urls []url.URL) error {
	errs := []error{}
	for _, u := range urls {
		if u.Scheme != "http" && u.Scheme != "https" {
			errs = append(errs, fmt.Errorf("invalid registry url scheme must be http or https: %s", u.String()))
		}
		if u.Path != "" {
			errs = append(errs, fmt.Errorf("invalid registry url path has to be empty: %s", u.String()))
		}
		if len(u.Query()) != 0 {
			errs = append(errs, fmt.Errorf("invalid registry url query has to be empty: %s", u.String()))
		}
		if u.User != nil {
			errs = append(errs, fmt.Errorf("invalid registry url user has to be empty: %s", u.String()))
		}
	}
	return errors.Join(errs...)
}
