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
	"reflect"
	"strings"

	"github.com/containerd/containerd"
	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pelletier/go-toml/v2"
	tomlu "github.com/pelletier/go-toml/v2/unstable"
	"github.com/spf13/afero"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/spegel-org/spegel/internal/channel"
)

const (
	backupDir = "_backup"
)

var _ Client = &Containerd{}

type Containerd struct {
	contentPath        string
	client             *containerd.Client
	clientGetter       func() (*containerd.Client, error)
	listFilter         string
	eventFilter        string
	registryConfigPath string
}

type Option func(*Containerd)

func WithContentPath(path string) Option {
	return func(c *Containerd) {
		c.contentPath = path
	}
}

func NewContainerd(sock, namespace, registryConfigPath string, registries []url.URL, opts ...Option) (*Containerd, error) {
	listFilter, eventFilter := createFilters(registries)
	c := &Containerd{
		clientGetter: func() (*containerd.Client, error) {
			return containerd.New(sock, containerd.WithDefaultNamespace(namespace))
		},
		listFilter:         listFilter,
		eventFilter:        eventFilter,
		registryConfigPath: registryConfigPath,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
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
		return errors.New("could not reach Containerd service")
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
		return errors.New("could not get config data from info response")
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
		return errors.New("Containerd discard unpacked layers cannot be enabled")
	}
	if cfg.Registry.ConfigPath == "" {
		return errors.New("Containerd registry config path needs to be set for mirror configuration to take effect")
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

func (c *Containerd) Subscribe(ctx context.Context) (<-chan ImageEvent, <-chan error, error) {
	imgCh := make(chan ImageEvent)
	errCh := make(chan error)
	client, err := c.Client()
	if err != nil {
		return nil, nil, err
	}
	envelopeCh, cErrCh := client.EventService().Subscribe(ctx, c.eventFilter)
	go func() {
		defer func() {
			close(imgCh)
			close(errCh)
		}()
		for envelope := range envelopeCh {
			var img Image
			imageName, eventType, err := getEventImage(envelope.Event)
			if err != nil {
				errCh <- err
				continue
			}
			switch eventType {
			case CreateEvent, UpdateEvent:
				cImg, err := client.GetImage(ctx, imageName)
				if err != nil {
					errCh <- err
					continue
				}
				img, err = Parse(cImg.Name(), cImg.Target().Digest)
				if err != nil {
					errCh <- err
					continue
				}
			case DeleteEvent:
				img, err = Parse(imageName, "")
				if err != nil {
					errCh <- err
					continue
				}
			}
			imgCh <- ImageEvent{Image: img, Type: eventType}
		}
	}()
	return imgCh, channel.Merge(errCh, cErrCh), nil
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
	if errors.Is(err, errdefs.ErrNotFound) {
		return 0, errors.Join(ErrNotFound, err)
	}
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
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, "", errors.Join(ErrNotFound, err)
	}
	if err != nil {
		return nil, "", err
	}
	mt, err := DetermineMediaType(b)
	if err != nil {
		return nil, "", err
	}
	return b, mt, nil
}

func (c *Containerd) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	if c.contentPath != "" {
		path := filepath.Join(c.contentPath, "blobs", dgst.Algorithm().String(), dgst.Encoded())
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(ErrNotFound, err)
		}
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	ra, err := client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, errors.Join(ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: content.NewReader(ra),
		Closer: ra,
	}, nil
}

func getEventImage(e typeurl.Any) (string, EventType, error) {
	if e == nil {
		return "", "", errors.New("any cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(e)
	if err != nil {
		return "", "", fmt.Errorf("failed to unmarshal any: %w", err)
	}
	switch e := evt.(type) {
	case *eventtypes.ImageCreate:
		return e.Name, CreateEvent, nil
	case *eventtypes.ImageUpdate:
		return e.Name, UpdateEvent, nil
	case *eventtypes.ImageDelete:
		return e.Name, DeleteEvent, nil
	default:
		return "", "", errors.New("unsupported event type")
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

type hostFile struct {
	HostConfigs map[string]hostConfig `toml:"host"`
	Server      string                `toml:"server"`

	orderedHosts []string `toml:"-"`
}

type hostFileOut struct {
	HostConfigs any    `toml:"host"`
	Server      string `toml:"server"`
}

type hostConfig struct {
	CACert       interface{}            `toml:"ca"`
	Client       interface{}            `toml:"client"`
	OverridePath *bool                  `toml:"override_path"`
	SkipVerify   *bool                  `toml:"skip_verify"`
	Header       map[string]interface{} `toml:"header"`
	Capabilities []string               `toml:"capabilities"`
}

// Refer to containerd registry configuration documentation for more information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs, mirrorURLs []url.URL, resolveTags, appendToBackup bool) error {
	log := logr.FromContextOrDiscard(ctx)
	err := validateRegistries(registryURLs)
	if err != nil {
		return err
	}
	err = fs.MkdirAll(configPath, 0o755)
	if err != nil {
		return err
	}
	err = backupConfig(log, fs, configPath)
	if err != nil {
		return err
	}
	err = clearConfig(fs, configPath)
	if err != nil {
		return err
	}

	// Write mirror configuration
	capabilities := []string{"pull"}
	if resolveTags {
		capabilities = append(capabilities, "resolve")
	}

	hostConfigType := reflect.TypeFor[hostConfig]()
	for _, registryURL := range registryURLs {
		hf, appending, err := getHostFile(fs, configPath, appendToBackup, registryURL)
		if err != nil {
			return err
		}

		// We make a struct to represent the hosts which allow us to control the order of the hosts in the output toml table
		// Hosts order is important as containerd will try them in the order of apparition
		// The order with a map would be unspecified while the go-toml lib guarantees the same order as the struct fields
		hostConfigSize := len(registryURLs) + len(hf.HostConfigs)
		hostConfigFields := make([]reflect.StructField, 0, hostConfigSize)
		hostConfigValues := make([]hostConfig, 0, hostConfigSize)
		addHost := func(host string, config hostConfig) {
			hostConfigFields = append(hostConfigFields, reflect.StructField{
				Name: fmt.Sprintf("F%d", len(hostConfigFields)),
				Type: hostConfigType,
				Tag:  reflect.StructTag(fmt.Sprintf("toml:%q", host)),
			})
			hostConfigValues = append(hostConfigValues, config)
		}
		for _, u := range mirrorURLs {
			addHost(u.String(), hostConfig{Capabilities: capabilities})
		}
		for _, h := range hf.orderedHosts {
			addHost(h, hf.HostConfigs[h])
		}

		// Instantiate struct and assign values
		hostsConfigs := reflect.New(reflect.StructOf(hostConfigFields))
		for i, v := range hostConfigValues {
			f := hostsConfigs.Elem().Field(i)
			f.Set(reflect.ValueOf(v))
		}

		out := &hostFileOut{
			Server: hf.Server,
		}
		reflect.ValueOf(&out.HostConfigs).Elem().Set(hostsConfigs)

		b, err := toml.Marshal(out)
		if err != nil {
			return err
		}
		fp := path.Join(configPath, registryURL.Host, "hosts.toml")
		err = fs.MkdirAll(path.Dir(fp), 0o755)
		if err != nil {
			return err
		}
		err = afero.WriteFile(fs, fp, b, 0o644)
		if err != nil {
			return err
		}
		if appending {
			log.Info("appending to existing Containerd mirror configuration", "registry", registryURL.String(), "path", fp)
		} else {
			log.Info("added Containerd mirror configuration", "registry", registryURL.String(), "path", fp)
		}
	}
	return nil
}

func validateRegistries(urls []url.URL) error {
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

func backupConfig(log logr.Logger, fs afero.Fs, configPath string) error {
	backupDirPath := path.Join(configPath, backupDir)
	_, err := fs.Stat(backupDirPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		return nil
	}
	files, err := afero.ReadDir(fs, configPath)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	err = fs.MkdirAll(backupDirPath, 0o755)
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
	return nil
}

func clearConfig(fs afero.Fs, configPath string) error {
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
	return nil
}

func getHostFile(fs afero.Fs, configPath string, appendToBackup bool, registryURL url.URL) (hostFile, bool, error) {
	if appendToBackup {
		fp := path.Join(configPath, backupDir, registryURL.Host, "hosts.toml")
		b, err := afero.ReadFile(fs, fp)
		if err != nil && !errors.Is(err, afero.ErrFileNotFound) {
			return hostFile{}, false, err
		}
		if err == nil {
			hf := hostFile{}
			err := toml.Unmarshal(b, &hf)
			if err != nil {
				return hostFile{}, false, err
			}
			hf.orderedHosts = getOrderedHosts(b)
			return hf, true, nil
		}
	}
	server := registryURL.String()
	if registryURL.String() == "https://docker.io" {
		server = "https://registry-1.docker.io"
	}
	hf := hostFile{
		Server:      server,
		HostConfigs: map[string]hostConfig{},
	}
	return hf, false, nil
}

// getOrderedHosts parses the given byte slice as TOML data and returns a slice of
// strings containing the ordered hosts found in the TOML data.
func getOrderedHosts(b []byte) []string {
	p := tomlu.Parser{}
	p.Reset(b)
	orderedHosts := []string{}
	for p.NextExpression() {
		e := p.Expression()
		if e.Kind != tomlu.Table {
			continue
		}
		ki := e.Key()
		// check if the expression is a host ("[host.'xxx']")
		if ki.Next() && string(ki.Node().Data) == "host" &&
			ki.Next() && ki.IsLast() {
			orderedHosts = append(orderedHosts, string(ki.Node().Data))
		}
	}
	return orderedHosts
}
