package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/xenitab/pkg/channels"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const (
	backupDir = "_backup"
)

var _ Client = &Containerd{}

type Containerd struct {
	client             *containerd.Client
	clientGetter       func() (*containerd.Client, error)
	listFilter         string
	eventFilter        string
	registryConfigPath string
}

func NewContainerd(sock, namespace, registryConfigPath string, registries []url.URL) (*Containerd, error) {
	listFilter, eventFilter := createFilters(registries)
	return &Containerd{
		clientGetter: func() (*containerd.Client, error) {
			return containerd.New(sock, containerd.WithDefaultNamespace(namespace))
		},
		listFilter:         listFilter,
		eventFilter:        eventFilter,
		registryConfigPath: registryConfigPath,
	}, nil
}

func (c *Containerd) Client() (*containerd.Client, error) {
	var err error
	if c.client == nil {
		c.client, err = c.clientGetter()
	}
	return c.client, err
}

func (c *Containerd) Name() string {
	return "containerd"
}

func (c *Containerd) Verify(ctx context.Context) error {
	client, err := c.Client()
	if err != nil {
		return err
	}
	ok, err := client.IsServing(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("could not reach Containerd service")
	}
	resp, err := runtimeapi.NewRuntimeServiceClient(client.Conn()).Status(ctx, &runtimeapi.StatusRequest{Verbose: true})
	if err != nil {
		return err
	}
	err = verifyStatusResponse(resp, c.registryConfigPath)
	if err != nil {
		return err
	}
	return nil
}

func verifyStatusResponse(resp *runtimeapi.StatusResponse, configPath string) error {
	str, ok := resp.Info["config"]
	if !ok {
		return fmt.Errorf("could not get config data from info response")
	}
	cfg := &struct {
		Registry struct {
			ConfigPath string `json:"configPath"`
		} `json:"registry"`
		Containerd struct {
			Runtimes struct {
				DiscardUnpackedLayers bool `json:"discardUnpackedLayers"`
			} `json:"runtimes"`
		} `json:"containerd"`
	}{}
	err := json.Unmarshal([]byte(str), cfg)
	if err != nil {
		return err
	}
	if cfg.Containerd.Runtimes.DiscardUnpackedLayers {
		return fmt.Errorf("Containerd discard unpacked layers cannot be enabled")
	}
	if cfg.Registry.ConfigPath == "" {
		return fmt.Errorf("Containerd registry config path needs to be set for mirror configuration to take effect")
	}
	paths := filepath.SplitList(cfg.Registry.ConfigPath)
	for _, path := range paths {
		if path != configPath {
			continue
		}
		return nil
	}
	return fmt.Errorf("Containerd registry config path is %s but needs to contain path %s for mirror configuration to take effect", cfg.Registry.ConfigPath, configPath)
}

func (c *Containerd) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error) {
	imgCh := make(chan ImageEvent, 1)
	errCh := make(chan error, 1)

	client, err := c.Client()
	if err != nil {
		errCh <- err
		close(imgCh)
		close(errCh)
		return imgCh, errCh
	}

	envelopeCh, cErrCh := client.EventService().Subscribe(ctx, c.eventFilter)
	go func() {
		for envelope := range envelopeCh {
			var img Image
			imageName, eventType, err := getEventImage(envelope.Event)
			if err != nil {
				errCh <- err
				return
			}
			switch eventType {
			case CreateEvent, UpdateEvent:
				cImg, err := client.GetImage(ctx, imageName)
				if err != nil {
					errCh <- err
					return
				}
				img, err = Parse(cImg.Name(), cImg.Target().Digest)
				if err != nil {
					errCh <- err
					return
				}
			case DeleteEvent:
				img, err = Parse(imageName, "")
				if err != nil {
					errCh <- err
					return
				}
			}
			imgCh <- ImageEvent{Image: img, Type: eventType}
		}
	}()
	return imgCh, channels.Merge(errCh, cErrCh)
}

func (c *Containerd) ListImages(ctx context.Context) ([]Image, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	cImgs, err := client.ListImages(ctx, c.listFilter)
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

func (c *Containerd) AllIdentifiers(ctx context.Context, img Image) ([]string, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	cImg, err := client.ImageService().Get(ctx, img.Name)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	err = images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		keys = append(keys, desc.Digest.String())
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			var idx ocispec.Index
			b, err := content.ReadBlob(ctx, client.ContentStore(), desc)
			if err != nil {
				return nil, fmt.Errorf("failed to read blob for manifest list: %w", err)
			}
			if err := json.Unmarshal(b, &idx); err != nil {
				return nil, err
			}
			var descs []ocispec.Descriptor
			for _, m := range idx.Manifests {
				// Skip index layers that do not exist locally
				if _, err := client.ContentStore().Info(ctx, m.Digest); err != nil {
					continue
				}
				descs = append(descs, m)
			}
			if len(descs) == 0 {
				return nil, fmt.Errorf("could not find any platforms with local content in manifest list: %v", desc.Digest)
			}
			return descs, nil
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			var manifest ocispec.Manifest
			b, err := content.ReadBlob(ctx, client.ContentStore(), desc)
			if err != nil {
				return nil, fmt.Errorf("failed to read blob for manifest: %w", err)
			}
			if err := json.Unmarshal(b, &manifest); err != nil {
				return nil, err
			}
			keys = append(keys, manifest.Config.Digest.String())
			for _, layer := range manifest.Layers {
				keys = append(keys, layer.Digest.String())
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected media type %v for digest: %v", desc.MediaType, desc.Digest)
		}
	}), cImg.Target)
	if err != nil {
		return nil, fmt.Errorf("failed to walk image manifests: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no image digests found")
	}
	return keys, nil
}

func (c *Containerd) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	client, err := c.Client()
	if err != nil {
		return "", err
	}
	cImg, err := client.GetImage(ctx, ref)
	if err != nil {
		return "", err
	}
	return cImg.Target().Digest, nil
}

func (c *Containerd) Size(ctx context.Context, dgst digest.Digest) (int64, error) {
	client, err := c.Client()
	if err != nil {
		return 0, err
	}
	info, err := client.ContentStore().Info(ctx, dgst)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (c *Containerd) GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error) {
	client, err := c.Client()
	if err != nil {
		return nil, "", err
	}
	b, err := content.ReadBlob(ctx, client.ContentStore(), ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return nil, "", err
	}
	var ud UnknownDocument
	if err := json.Unmarshal(b, &ud); err != nil {
		return nil, "", err
	}
	if ud.MediaType != "" {
		return b, ud.MediaType, nil
	}
	var ic ocispec.Image
	if err := json.Unmarshal(b, &ic); err != nil {
		return nil, "", err
	}
	if isImageConfig(ic) {
		return b, ocispec.MediaTypeImageConfig, nil
	}
	// Media type is not a required field. We need a fallback method if the field is not set.
	mt, err := c.lookupMediaType(ctx, dgst)
	if err != nil {
		return nil, "", fmt.Errorf("could not get media type for %s: %w", dgst.String(), err)
	}
	return b, mt, nil
}

func (c *Containerd) CopyLayer(ctx context.Context, dgst digest.Digest, dst io.Writer, bsize int) error {
	client, err := c.Client()
	if err != nil {
		return err
	}
	ra, err := client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return err
	}
	defer ra.Close()

	startTime := time.Now()

	// Use a buffer to improve copy performance
	buffer := make([]byte, bsize)

	wSize, err := io.CopyBuffer(dst, content.NewReader(ra), buffer)
	duration := time.Since(startTime)

	if err != nil {
		return err
	} else {
		fmt.Printf("Blob io.CopyBuffer completed in %s, blob size is %v", duration, wSize)
	}

	return nil
}

// lookupMediaType will resolve the media type for a digest without looking at the content.
// Only use this as a fallback method as it is a lot slower than reading it from the file.
// TODO: A cache would be helpful to speed up lookups for the same digets.
func (c *Containerd) lookupMediaType(ctx context.Context, dgst digest.Digest) (string, error) {
	logr.FromContextOrDiscard(ctx).Info("using Containerd fallback method to determine media type", "digest", dgst.String())
	client, err := c.Client()
	if err != nil {
		return "", err
	}
	images, err := client.ImageService().List(ctx, fmt.Sprintf("target.digest==%s", dgst.String()))
	if err != nil {
		return "", err
	}
	if len(images) > 0 {
		return images[0].Target.MediaType, nil
	}
	info, err := client.ContentStore().Info(ctx, dgst)
	if err != nil {
		return "", err
	}
	filter, err := getContentFilter(info.Labels)
	if err != nil {
		return "", err
	}
	var parentDgst digest.Digest
	err = client.ContentStore().Walk(ctx, func(info content.Info) error {
		for _, v := range info.Labels {
			if v != dgst.String() {
				continue
			}
			parentDgst = info.Digest
			return filepath.SkipAll
		}
		return nil
	}, filter)
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return "", err
	}
	if parentDgst == "" {
		return "", fmt.Errorf("could not find parent")
	}
	b, err := content.ReadBlob(ctx, client.ContentStore(), ocispec.Descriptor{Digest: parentDgst})
	if err != nil {
		return "", err
	}
	var idx ocispec.Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return "", err
	}
	for _, desc := range idx.Manifests {
		if desc.Digest == dgst {
			return desc.MediaType, nil
		}
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return "", err
	}
	if manifest.Config.Digest == dgst {
		return manifest.Config.MediaType, nil
	}
	for _, layer := range manifest.Layers {
		if layer.Digest == dgst {
			return layer.MediaType, nil
		}
	}
	return "", fmt.Errorf("could not find reference in parent")
}

func getContentFilter(labels map[string]string) (string, error) {
	for k, v := range labels {
		if !strings.HasPrefix(k, "containerd.io/distribution.source") {
			continue
		}
		return fmt.Sprintf(`labels."%s"==%s`, k, v), nil
	}
	return "", fmt.Errorf("could not find distribution label to create content filter")
}

func getEventImage(e typeurl.Any) (string, EventType, error) {
	evt, err := typeurl.UnmarshalAny(e)
	if err != nil {
		return "", UnknownEvent, fmt.Errorf("failed to unmarshalany: %w", err)
	}
	switch e := evt.(type) {
	case *eventtypes.ImageCreate:
		return e.Name, CreateEvent, nil
	case *eventtypes.ImageUpdate:
		return e.Name, UpdateEvent, nil
	case *eventtypes.ImageDelete:
		return e.Name, DeleteEvent, nil
	default:
		return "", UnknownEvent, errors.New("unsupported event")
	}
}

func createFilters(registries []url.URL) (string, string) {
	registryHosts := []string{}
	for _, registry := range registries {
		registryHosts = append(registryHosts, strings.ReplaceAll(registry.Host, `.`, `\\.`))
	}
	listFilter := fmt.Sprintf(`name~="^(%s)/"`, strings.Join(registryHosts, "|"))
	eventFilter := fmt.Sprintf(`topic~="/images/create|/images/update|/images/delete",event.%s`, listFilter)
	return listFilter, eventFilter
}

func isImageConfig(ic ocispec.Image) bool {
	if ic.Architecture == "" {
		return false
	}
	if ic.OS == "" {
		return false
	}
	if ic.RootFS.Type == "" {
		return false
	}
	return true
}

type hostFile struct {
	HostConfigs map[string]hostConfig `toml:"host"`
	Server      string                `toml:"server"`
}

type hostConfig struct {
	Capabilities []string `toml:"capabilities"`
}

// Refer to containerd registry configuration documentation for mor information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs, mirrorURLs []url.URL, resolveTags bool) error {
	log := logr.FromContextOrDiscard(ctx)

	if err := validate(registryURLs); err != nil {
		return err
	}

	// Create config path dir if it does not exist
	ok, err := afero.DirExists(fs, configPath)
	if err != nil {
		return err
	}
	if !ok {
		err := fs.MkdirAll(configPath, 0755)
		if err != nil {
			return err
		}
	}

	// Backup files and directories in config path
	backupDirPath := path.Join(configPath, backupDir)
	if _, err := fs.Stat(backupDirPath); os.IsNotExist(err) {
		files, err := afero.ReadDir(fs, configPath)
		if err != nil {
			return err
		}
		if len(files) > 0 {
			err = fs.MkdirAll(backupDirPath, 0755)
			if err != nil {
				return err
			}
			for _, fi := range files {
				oldPath := path.Join(configPath, fi.Name())
				newPath := path.Join(backupDirPath, fi.Name())
				err := fs.Rename(oldPath, newPath)
				if err != nil {
					return err
				}
				log.Info("backing up Containerd host configuration", "path", oldPath)
			}
		}
	}

	// Remove all content from config path to start from clean slate
	files, err := afero.ReadDir(fs, configPath)
	if err != nil {
		return err
	}
	for _, fi := range files {
		if fi.Name() == backupDir {
			continue
		}
		filePath := path.Join(configPath, fi.Name())
		err := fs.RemoveAll(filePath)
		if err != nil {
			return err
		}
	}

	// Write mirror configuration
	capabilities := []string{"pull"}
	if resolveTags {
		capabilities = append(capabilities, "resolve")
	}
	for _, registryURL := range registryURLs {
		// Need a special case for Docker Hub as docker.io is just an alias.
		server := registryURL.String()
		if registryURL.String() == "https://docker.io" {
			server = "https://registry-1.docker.io"
		}
		hostConfigs := map[string]hostConfig{}
		for _, u := range mirrorURLs {
			hostConfigs[u.String()] = hostConfig{Capabilities: capabilities}
		}
		cfg := hostFile{
			Server:      server,
			HostConfigs: hostConfigs,
		}
		b, err := toml.Marshal(&cfg)
		if err != nil {
			return err
		}
		fp := path.Join(configPath, registryURL.Host, "hosts.toml")
		err = fs.MkdirAll(path.Dir(fp), 0755)
		if err != nil {
			return err
		}
		err = afero.WriteFile(fs, fp, b, 0644)
		if err != nil {
			return err
		}
		log.Info("added containerd mirror configuration", "registry", registryURL.String(), "path", fp)
	}
	return nil
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
