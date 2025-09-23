package oci

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pelletier/go-toml/v2"
	tomlu "github.com/pelletier/go-toml/v2/unstable"
	"google.golang.org/grpc"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const (
	backupDir              = "_backup"
	wildcardRegistryMirror = "_default"
)

type Feature uint32

const (
	FeatureConfigCheck Feature = 1 << iota
	FeatureContentEvent
)

func (f *Feature) Set(feat Feature) {
	*f |= feat
}

func (f Feature) Has(feat Feature) bool {
	return f&feat != 0
}

func (f Feature) String() string {
	feats := []string{}
	if f.Has(FeatureConfigCheck) {
		feats = append(feats, "ConfigCheck")
	}
	if f.Has(FeatureContentEvent) {
		feats = append(feats, "ContentEvent")
	}
	return strings.Join(feats, "|")
}

type ContainerdConfig struct {
	ContentPath string
}

func (cfg *ContainerdConfig) Apply(opts ...ContainerdOption) error {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}

type ContainerdOption func(*ContainerdConfig) error

func WithContentPath(path string) ContainerdOption {
	return func(c *ContainerdConfig) error {
		c.ContentPath = path
		return nil
	}
}

var _ Store = &Containerd{}

type Containerd struct {
	contentPath        string
	registryConfigPath string
	client             *client.Client
	clientGetter       func() (*client.Client, error)
	features           *Feature
	imageFilter        []string
	eventFilter        []string
	contentFilter      []string
}

func NewContainerd(sock, namespace, registryConfigPath string, mirroredRegistries []string, opts ...ContainerdOption) (*Containerd, error) {
	cfg := &ContainerdConfig{}
	err := cfg.Apply(opts...)
	if err != nil {
		return nil, err
	}

	parsedMirroredRegistries, err := parseMirroredRegistries(mirroredRegistries)
	if err != nil {
		return nil, err
	}
	imageFilter, eventFilter, contentFilter := createFilters(parsedMirroredRegistries)

	c := &Containerd{
		contentPath: cfg.ContentPath,
		clientGetter: func() (*client.Client, error) {
			return client.New(sock, client.WithDefaultNamespace(namespace))
		},
		imageFilter:        imageFilter,
		eventFilter:        eventFilter,
		contentFilter:      contentFilter,
		registryConfigPath: registryConfigPath,
	}
	return c, nil
}

func (c *Containerd) Client() (*client.Client, error) {
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
	log := logr.FromContextOrDiscard(ctx)
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

	feats, err := c.Features(ctx)
	if err != nil {
		return err
	}
	if !feats.Has(FeatureConfigCheck) {
		log.Info("skipping verification of Containerd configuration")
		return nil
	}
	grpcConn, ok := client.Conn().(*grpc.ClientConn)
	if !ok {
		return errors.New("client connection is not grpc")
	}
	srv := runtimeapi.NewRuntimeServiceClient(grpcConn)
	statusResp, err := srv.Status(ctx, &runtimeapi.StatusRequest{Verbose: true})
	if err != nil {
		return err
	}
	err = verifyStatusResponse(statusResp, c.registryConfigPath)
	if err != nil {
		return err
	}
	return nil
}

func (c *Containerd) Features(ctx context.Context) (Feature, error) {
	if c.features != nil {
		return *c.features, nil
	}

	log := logr.FromContextOrDiscard(ctx)
	client, err := c.Client()
	if err != nil {
		return 0, err
	}
	grpcConn, ok := client.Conn().(*grpc.ClientConn)
	if !ok {
		return 0, errors.New("client connection is not grpc")
	}
	srv := runtimeapi.NewRuntimeServiceClient(grpcConn)
	versionResp, err := srv.Version(ctx, &runtimeapi.VersionRequest{})
	if err != nil {
		return 0, err
	}
	feats, err := featuresForVersion(versionResp.RuntimeVersion)
	if err != nil {
		return 0, err
	}
	log.Info("setting features for Containerd version", "features", feats.String(), "version", versionResp.String())
	c.features = &feats
	return feats, nil
}

func featuresForVersion(version string) (Feature, error) {
	v, err := utilversion.Parse(version)
	if err != nil {
		return 0, fmt.Errorf("could not parse version %s: %w", version, err)
	}
	feats := Feature(0)
	if v.LessThan(utilversion.MustParse("2.0")) {
		feats.Set(FeatureConfigCheck)
	}
	if v.AtLeast(utilversion.MustParse("2.1")) {
		feats.Set(FeatureContentEvent)
	}
	return feats, nil
}

func verifyStatusResponse(resp *runtimeapi.StatusResponse, configPath string) error {
	str, ok := resp.Info["config"]
	if !ok {
		return errors.New("could not get config data from info response")
	}
	cfg := &struct {
		Registry struct {
			ConfigPath *string `json:"configPath"`
		} `json:"registry"`
		Containerd struct {
			DiscardUnpackedLayers *bool `json:"discardUnpackedLayers"`
		} `json:"containerd"`
	}{}
	err := json.Unmarshal([]byte(str), cfg)
	if err != nil {
		return err
	}
	if cfg.Containerd.DiscardUnpackedLayers == nil {
		return errors.New("field containerd.discardUnpackedLayers missing from config")
	}
	if *cfg.Containerd.DiscardUnpackedLayers {
		return errors.New("Containerd discard unpacked layers cannot be enabled")
	}
	if cfg.Registry.ConfigPath == nil {
		return errors.New("field registry.configPath missing from config")
	}
	if *cfg.Registry.ConfigPath == "" {
		return errors.New("Containerd registry config path needs to be set for mirror configuration to take effect")
	}
	paths := filepath.SplitList(*cfg.Registry.ConfigPath)
	for _, path := range paths {
		if path != configPath {
			continue
		}
		return nil
	}
	return fmt.Errorf("Containerd registry config path is %s but needs to contain path %s for mirror configuration to take effect", *cfg.Registry.ConfigPath, configPath)
}

func (c *Containerd) Subscribe(ctx context.Context) (<-chan OCIEvent, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	log := logr.FromContextOrDiscard(ctx)

	ctx, cancel := context.WithCancel(ctx)
	eventCh := make(chan OCIEvent)
	envelopeCh, cErrCh := client.EventService().Subscribe(ctx, c.eventFilter...)
	go func() {
		defer close(eventCh)
		for {
			select {
			case <-ctx.Done():
				return
			case envelope := <-envelopeCh:
				events, err := c.convertEvent(ctx, *envelope)
				if err != nil {
					log.Error(err, "error when handling event")
					continue
				}
				for _, event := range events {
					eventCh <- event
				}
			}
		}
	}()
	go func() {
		// Required so that the event channel closes in case Containerd is restarted.
		defer cancel()
		for err := range cErrCh {
			log.Error(err, "containerd event error")
		}
	}()
	return eventCh, nil
}

func (c *Containerd) ListImages(ctx context.Context) ([]Image, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	cImgs, err := client.ListImages(ctx, c.imageFilter...)
	if err != nil {
		return nil, err
	}
	imgs := []Image{}
	for _, cImg := range cImgs {
		img, err := ParseImage(cImg.Name(), WithDigest(cImg.Target().Digest))
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

func (c *Containerd) ListContents(ctx context.Context) ([]Content, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	contents := []Content{}
	err = client.ContentStore().Walk(ctx, func(i content.Info) error {
		registries := parseContentRegistries(i.Labels)
		content := Content{
			Digest:     i.Digest,
			Registires: registries,
		}
		contents = append(contents, content)
		return nil
	}, c.contentFilter...)
	if err != nil {
		return nil, err
	}
	return contents, nil
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

func (c *Containerd) GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
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
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: io.NewSectionReader(ra, 0, ra.Size()),
		Closer:     ra,
	}, nil
}

func (c *Containerd) convertEvent(ctx context.Context, envelope events.Envelope) ([]OCIEvent, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}

	if envelope.Event == nil {
		return nil, errors.New("envelope event cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope event: %w", err)
	}
	switch e := evt.(type) {
	case *eventtypes.ContentCreate:
		filter, err := filters.ParseAll(c.contentFilter...)
		if err != nil {
			return nil, err
		}
		dgst, err := digest.Parse(e.GetDigest())
		if err != nil {
			return nil, err
		}
		info, err := client.ContentStore().Info(ctx, dgst)
		if err != nil {
			return nil, err
		}
		if len(info.Labels) != 0 && !filter.Match(content.AdaptInfo(info)) {
			return nil, nil
		}
		return []OCIEvent{{Type: CreateEvent, Key: e.GetDigest()}}, nil
	case *eventtypes.ImageCreate:
		img, err := ParseImage(e.GetName(), AllowTagOnly())
		if err != nil {
			return nil, err
		}
		// Pull by tag creates an event only for the tag. We dont get content to avoid advertising twice.
		if img.Digest == "" {
			return []OCIEvent{{Type: CreateEvent, Key: e.GetName()}}, nil
		}
		// If Containerd supports content events we can skip walking the image.
		feats, err := c.Features(ctx)
		if err != nil {
			return nil, err
		}
		if feats.Has(FeatureContentEvent) {
			return nil, nil
		}
		dgsts, err := WalkImage(ctx, c, img)
		if err != nil {
			return nil, fmt.Errorf("could not get digests for image %s: %w", img.String(), err)
		}
		events := []OCIEvent{}
		for _, dgst := range dgsts {
			events = append(events, OCIEvent{Type: CreateEvent, Key: dgst.String()})
		}
		return events, nil
	case *eventtypes.ImageDelete:
		return []OCIEvent{{Type: DeleteEvent, Key: e.GetName()}}, nil
	default:
		return nil, errors.New("unsupported event type")
	}
}

func parseContentRegistries(l map[string]string) []string {
	registries := []string{}
	for k := range l {
		if !strings.HasPrefix(k, labels.LabelDistributionSource) {
			continue
		}
		registries = append(registries, strings.TrimPrefix(k, labels.LabelDistributionSource+"."))
	}
	return registries
}

func createFilters(parsedMirroredRegistries []url.URL) ([]string, []string, []string) {
	registryHosts := []string{}
	for _, registry := range parsedMirroredRegistries {
		registryHosts = append(registryHosts, strings.ReplaceAll(registry.Host, `.`, `\\.`))
	}
	imageFilter := fmt.Sprintf(`name~="^(%s)/"`, strings.Join(registryHosts, "|"))
	if len(registryHosts) == 0 {
		// Filter images that do not have a registry in it's reference,
		// as we cant mirror images without registries.
		imageFilter = `name~="^.+/"`
	}
	eventFilter := fmt.Sprintf(`topic~="/images/create|/images/delete",event.%s`, imageFilter)
	contentFilters := []string{}
	for _, registry := range parsedMirroredRegistries {
		contentFilters = append(contentFilters, fmt.Sprintf(`labels."%s.%s"~="^."`, labels.LabelDistributionSource, registry.Host))
	}
	return []string{imageFilter}, []string{eventFilter, `topic~="/content/create"`}, contentFilters
}

// Refer to containerd registry configuration documentation for more information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, configPath string, mirroredRegistries, mirrorTargets []string, resolveTags, prependExisting bool, username, password string) error {
	log := logr.FromContextOrDiscard(ctx)

	// Parse and verify mirror urls.
	if len(mirroredRegistries) == 0 {
		mirroredRegistries = append(mirroredRegistries, wildcardRegistryMirror)
	}
	parsedMirroredRegistries, err := parseMirroredRegistries(mirroredRegistries)
	if err != nil {
		return err
	}
	parsedMirrorTargets, err := parseMirrorTargets(mirrorTargets)
	if err != nil {
		return err
	}

	// Backup and clear configgurrationn.
	err = os.MkdirAll(configPath, 0o755)
	if err != nil {
		return err
	}
	err = backupConfig(log, configPath)
	if err != nil {
		return err
	}
	err = clearConfig(configPath)
	if err != nil {
		return err
	}

	// Write mirror configuration
	capabilities := []string{"pull"}
	if resolveTags {
		capabilities = append(capabilities, "resolve")
	}
	for _, mr := range parsedMirroredRegistries {
		templatedHosts, err := templateHosts(mr, parsedMirrorTargets, capabilities, username, password)
		if err != nil {
			return err
		}
		if prependExisting {
			existingHosts, err := existingHosts(configPath, mr)
			if err != nil {
				return err
			}
			if existingHosts != "" {
				templatedHosts = templatedHosts + "\n\n" + existingHosts
			}
			log.Info("prepending to existing Containerd mirror configuration", "registry", mr.String())
		}
		fp := path.Join(configPath, mr.Host, "hosts.toml")
		err = os.MkdirAll(filepath.Dir(fp), 0o755)
		if err != nil {
			return err
		}
		err = os.WriteFile(fp, []byte(templatedHosts), 0o644)
		if err != nil {
			return err
		}
		log.Info("added Containerd mirror configuration", "registry", mr.String(), "path", fp)
	}
	return nil
}

func CleanupMirrorConfiguration(ctx context.Context, configPath string) error {
	log := logr.FromContextOrDiscard(ctx)

	// If backup directory does not exist it means mirrors was never configured or cleanup has already run.
	backupDirPath := path.Join(configPath, backupDir)
	ok, err := dirExists(backupDirPath)
	if err != nil {
		return err
	}
	if !ok {
		log.Info("skipping cleanup because backup directory does not exist")
		return nil
	}

	// Remove everything except _backup
	err = clearConfig(configPath)
	if err != nil {
		return err
	}

	// Move content from backup directory
	files, err := os.ReadDir(backupDirPath)
	if err != nil {
		return err
	}
	for _, fi := range files {
		oldPath := path.Join(backupDirPath, fi.Name())
		newPath := path.Join(configPath, fi.Name())
		err := os.Rename(oldPath, newPath)
		if err != nil {
			return err
		}
		log.Info("recovering Containerd host configuration", "path", oldPath)
	}

	// Remove backup directory to indicate that cleanup has been run.
	err = os.RemoveAll(backupDirPath)
	if err != nil {
		return err
	}

	return nil
}

func parseMirroredRegistries(mirroredRegistries []string) ([]url.URL, error) {
	mru := []url.URL{}
	for _, s := range mirroredRegistries {
		if s == wildcardRegistryMirror {
			mru = append(mru, url.URL{Host: wildcardRegistryMirror})
			continue
		}
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		err = validateRegistry(u)
		if err != nil {
			return nil, err
		}
		mru = append(mru, *u)
	}
	return mru, nil
}

func parseMirrorTargets(mirroredTargets []string) ([]url.URL, error) {
	mru := []url.URL{}
	for _, s := range mirroredTargets {
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		err = validateRegistry(u)
		if err != nil {
			return nil, err
		}
		mru = append(mru, *u)
	}
	return mru, nil
}

func validateRegistry(u *url.URL) error {
	errs := []error{}
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
	return errors.Join(errs...)
}

func backupConfig(log logr.Logger, configPath string) error {
	backupDirPath := path.Join(configPath, backupDir)
	ok, err := dirExists(backupDirPath)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	files, err := os.ReadDir(configPath)
	if err != nil {
		return err
	}
	err = os.MkdirAll(backupDirPath, 0o755)
	if err != nil {
		return err
	}
	for _, fi := range files {
		oldPath := path.Join(configPath, fi.Name())
		newPath := path.Join(backupDirPath, fi.Name())
		err := os.Rename(oldPath, newPath)
		if err != nil {
			return err
		}
		log.Info("backing up Containerd host configuration", "path", oldPath)
	}
	return nil
}

func clearConfig(configPath string) error {
	files, err := os.ReadDir(configPath)
	if err != nil {
		return err
	}
	for _, fi := range files {
		if fi.Name() == backupDir {
			continue
		}
		filePath := path.Join(configPath, fi.Name())
		err := os.RemoveAll(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func templateHosts(parsedMirrorRegistry url.URL, parsedMirrorTargets []url.URL, capabilities []string, username, password string) (string, error) {
	server := parsedMirrorRegistry.String()
	if parsedMirrorRegistry.String() == "https://docker.io" {
		server = "https://registry-1.docker.io"
	}
	if parsedMirrorRegistry.Host == wildcardRegistryMirror {
		server = ""
	}

	authorization := ""
	if username != "" || password != "" {
		authorization = username + ":" + password
		authorization = base64.StdEncoding.EncodeToString([]byte(authorization))
		authorization = "Basic " + authorization
	}

	hc := struct {
		Authorization string
		Server        string
		Capabilities  string
		MirrorTargets []url.URL
	}{
		Server:        server,
		Capabilities:  fmt.Sprintf("['%s']", strings.Join(capabilities, "', '")),
		MirrorTargets: parsedMirrorTargets,
		Authorization: authorization,
	}
	tmpl, err := template.New("").Parse(`{{- with .Server }}server = '{{ . }}'{{ end }}
{{- $authorization := .Authorization }}
{{ range .MirrorTargets }}
[host.'{{ .String }}']
capabilities = {{ $.Capabilities }}
dial_timeout = '200ms'
{{- if $authorization }}
[host.'{{ .String }}'.header]
Authorization = '{{ $authorization }}'
{{- end }}
{{ end }}`)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBuffer(nil)
	err = tmpl.Execute(buf, hc)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

type hostFile struct {
	Hosts map[string]any `toml:"host"`
}

func existingHosts(configPath string, parsedMirrorRegistry url.URL) (string, error) {
	fp := path.Join(configPath, backupDir, parsedMirrorRegistry.Host, "hosts.toml")
	b, err := os.ReadFile(fp)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	var hf hostFile
	err = toml.Unmarshal(b, &hf)
	if err != nil {
		return "", err
	}
	if len(hf.Hosts) == 0 {
		return "", nil
	}

	hosts := []string{}
	parser := tomlu.Parser{}
	parser.Reset(b)
	for parser.NextExpression() {
		err := parser.Error()
		if err != nil {
			return "", err
		}
		e := parser.Expression()
		if e.Kind != tomlu.Table {
			continue
		}
		ki := e.Key()
		if ki.Next() && string(ki.Node().Data) == "host" && ki.Next() && ki.IsLast() {
			hosts = append(hosts, string(ki.Node().Data))
		}
	}

	ehs := []string{}
	for _, h := range hosts {
		data := hostFile{
			Hosts: map[string]any{
				h: hf.Hosts[h],
			},
		}
		b, err := toml.Marshal(data)
		if err != nil {
			return "", err
		}
		eh := strings.TrimPrefix(string(b), "[host]\n")
		ehs = append(ehs, eh)
	}
	return strings.TrimSpace(strings.Join(ehs, "\n")), nil
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}
