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
	"time"

	"github.com/avast/retry-go/v4"
	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pelletier/go-toml/v2"
	tomlu "github.com/pelletier/go-toml/v2/unstable"
	"google.golang.org/grpc"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	backupDir       = "_backup"
	listImageFilter = `name~="^.+/"`
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

type ContainerdOption = option.Option[ContainerdConfig]

func WithContentPath(path string) ContainerdOption {
	return func(c *ContainerdConfig) error {
		c.ContentPath = path
		return nil
	}
}

var _ Store = &Containerd{}

type Containerd struct {
	client       *client.Client
	mediaTypeIdx *lru.Cache[digest.Digest, string]
	contentPath  string
	features     Feature
}

func NewContainerd(ctx context.Context, sock, namespace string, opts ...ContainerdOption) (*Containerd, error) {
	cfg := ContainerdConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}

	client, err := client.New(sock, client.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, err
	}
	features, err := containerdFeatures(ctx, client)
	if err != nil {
		return nil, err
	}

	mediaTypeIdx, err := lru.New[digest.Digest, string](100)
	if err != nil {
		return nil, err
	}

	c := &Containerd{
		client:       client,
		mediaTypeIdx: mediaTypeIdx,
		contentPath:  cfg.ContentPath,
		features:     features,
	}
	return c, nil
}

func (c *Containerd) Verify(ctx context.Context, registryConfigPath string) error {
	err := verifyContainerd(ctx, c.client, c.features, registryConfigPath)
	if err != nil {
		return err
	}
	return nil
}

func (c *Containerd) Close() error {
	err := c.client.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *Containerd) Name() string {
	return "containerd"
}

func (c *Containerd) ListImages(ctx context.Context) ([]Image, error) {
	cImgs, err := c.client.ImageService().List(ctx, listImageFilter)
	if err != nil {
		return nil, err
	}
	imgs := []Image{}
	for _, cImg := range cImgs {
		img, err := ParseImage(cImg.Name, WithDigest(cImg.Target.Digest))
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

func (c *Containerd) ListContent(ctx context.Context) ([][]Reference, error) {
	contents := [][]Reference{}
	err := c.client.ContentStore().Walk(ctx, func(i content.Info) error {
		refs, err := contentLabelsToReferences(i.Labels, i.Digest)
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "skipping content that cant be converted to reference")
			return nil
		}
		contents = append(contents, refs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return contents, nil
}

func (c *Containerd) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	cImg, err := c.client.ImageService().Get(ctx, ref)
	if err != nil {
		return "", err
	}
	return cImg.Target.Digest, nil
}

func (c *Containerd) Descriptor(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if errors.Is(err, errdefs.ErrNotFound) {
		return ocispec.Descriptor{}, errors.Join(ErrNotFound, err)
	}
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	mt, ok := c.mediaTypeIdx.Get(dgst)
	if !ok {
		mt, err = func() (string, error) {
			if info.Size > ManifestMaxSize {
				return httpx.ContentTypeBinary, nil
			}
			rc, err := c.Open(ctx, dgst)
			if err != nil {
				return "", err
			}
			defer rc.Close()
			mt, err := FingerprintMediaType(rc)
			if err != nil {
				return "", err
			}
			return mt, nil
		}()
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		c.mediaTypeIdx.Add(dgst, mt)
	}

	desc := ocispec.Descriptor{
		Size:      info.Size,
		Digest:    dgst,
		MediaType: mt,
	}
	return desc, nil
}

func (c *Containerd) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
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
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
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

func (c *Containerd) Subscribe(ctx context.Context) (<-chan OCIEvent, error) {
	log := logr.FromContextOrDiscard(ctx)

	eventCh := make(chan OCIEvent)
	subCtx, subCancel := context.WithCancel(ctx)
	eventFilters := []string{`topic~="/images/create|/images/delete",event.name~="^.+/"`, `topic~="/content/create"`}
	envelopeCh, cErrCh := c.client.EventService().Subscribe(subCtx, eventFilters...)

	// Populate the content index.
	contentIdx := map[digest.Digest][]Reference{}
	cImgs, err := c.client.ImageService().List(ctx, listImageFilter)
	if err != nil {
		subCancel()
		return nil, err
	}
	for _, cImg := range cImgs {
		img, err := ParseImage(cImg.Name, WithDigest(cImg.Target.Digest))
		if err != nil {
			subCancel()
			return nil, err
		}
		refs := []Reference{}
		handler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			children, err := images.ChildrenHandler(c.client.ContentStore()).Handle(ctx, desc)
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			ref := Reference{
				Registry:   img.Registry,
				Repository: img.Repository,
				Digest:     desc.Digest,
			}
			refs = append(refs, ref)
			return children, nil
		})
		err = images.Walk(ctx, handler, cImg.Target)
		if err != nil {
			subCancel()
			return nil, err
		}
		contentIdx[cImg.Target.Digest] = refs
	}

	go func() {
		defer close(eventCh)
		for {
			select {
			case <-subCtx.Done():
				return
			case envelope := <-envelopeCh:
				events, err := c.handleEvent(subCtx, *envelope, contentIdx)
				if err != nil {
					log.Error(err, "error when handling Contianerd event")
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
		defer subCancel()
		for err := range cErrCh {
			log.Error(err, "received Containerd event error")
		}
	}()
	return eventCh, nil
}

func (c *Containerd) handleEvent(ctx context.Context, envelope events.Envelope, contentIdx map[digest.Digest][]Reference) ([]OCIEvent, error) {
	if envelope.Event == nil {
		return nil, errors.New("envelope event cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope event: %w", err)
	}
	switch e := evt.(type) {
	case *eventtypes.ContentCreate:
		dgst := digest.Digest(e.GetDigest())
		retryOpts := []retry.Option{
			retry.Context(ctx),
			retry.Attempts(10),
			retry.MaxDelay(100 * time.Millisecond),
		}
		refs, err := retry.DoWithData(func() ([]Reference, error) {
			info, err := c.client.ContentStore().Info(ctx, dgst)
			if err != nil {
				return nil, retry.Unrecoverable(err)
			}
			refs, err := contentLabelsToReferences(info.Labels, dgst)
			if err != nil {
				return nil, err
			}
			return refs, nil
		}, retryOpts...)
		if err != nil {
			return nil, err
		}
		events := []OCIEvent{}
		for _, ref := range refs {
			events = append(events, OCIEvent{Type: CreateEvent, Reference: ref})
		}
		return events, nil
	case *eventtypes.ImageCreate:
		img, err := ParseImage(e.GetName(), AllowTagOnly())
		if err != nil {
			return nil, err
		}
		// Just advertise the image if it is a tag reference.
		if img.Digest == "" {
			return []OCIEvent{{Type: CreateEvent, Reference: img.Reference}}, nil
		}
		// Walk the image to index its content.
		cImg, err := c.client.ImageService().Get(ctx, img.String())
		if err != nil {
			return nil, err
		}
		refs := []Reference{}
		events := []OCIEvent{}
		handler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			children, err := images.ChildrenHandler(c.client.ContentStore()).Handle(ctx, desc)
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			ref := Reference{
				Registry:   img.Registry,
				Repository: img.Repository,
				Digest:     desc.Digest,
			}
			refs = append(refs, ref)
			events = append(events, OCIEvent{Type: CreateEvent, Reference: ref})
			return children, nil
		})
		err = images.Walk(ctx, handler, cImg.Target)
		if err != nil {
			return nil, err
		}
		contentIdx[img.Digest] = refs
		// No need to advertise content if we receive content events.
		if c.features.Has(FeatureContentEvent) {
			return nil, nil
		}
		return events, nil
	case *eventtypes.ImageDelete:
		img, err := ParseImage(e.GetName(), AllowTagOnly())
		if err != nil {
			return nil, err
		}
		// Just advertise the image if it is a tag reference.
		if img.Digest == "" {
			return []OCIEvent{{Type: DeleteEvent, Reference: img.Reference}}, nil
		}
		// Advertise deletion of images content if it no longer exists.
		refs, ok := contentIdx[img.Digest]
		if !ok {
			logr.FromContextOrDiscard(ctx).Info("delete event with missing content index entry")
			return []OCIEvent{{Type: DeleteEvent, Reference: img.Reference}}, nil
		}
		delete(contentIdx, img.Digest)
		// Delete events are sent before garbage collection is run.
		retryOpts := []retry.Option{
			retry.Context(ctx),
			retry.Attempts(10),
			retry.MaxDelay(100 * time.Millisecond),
		}
		err = retry.Do(func() error {
			_, err := c.client.ContentStore().Info(ctx, img.Digest)
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil
			}
			if err != nil {
				return retry.Unrecoverable(err)
			}
			return fmt.Errorf("manifest with digest %s still exists", img.Digest.String())
		}, retryOpts...)
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "image manifest has not been deleted")
		}
		// Create delete events for contents that has been removed.
		events := []OCIEvent{}
		for _, ref := range refs {
			_, err := c.client.ContentStore().Info(ctx, ref.Digest)
			if err == nil {
				continue
			}
			if !errors.Is(err, errdefs.ErrNotFound) {
				return nil, err
			}
			events = append(events, OCIEvent{Type: DeleteEvent, Reference: ref})
		}
		return events, nil
	default:
		return nil, errors.New("unsupported event type")
	}
}

func containerdFeatures(ctx context.Context, client *client.Client) (Feature, error) {
	grpcConn, ok := client.Conn().(*grpc.ClientConn)
	if !ok {
		return 0, errors.New("client connection is not grpc")
	}
	srv := runtimeapi.NewRuntimeServiceClient(grpcConn)
	versionResp, err := srv.Version(ctx, &runtimeapi.VersionRequest{})
	if err != nil {
		return 0, err
	}
	features, err := featuresForVersion(versionResp.RuntimeVersion)
	if err != nil {
		return 0, err
	}
	return features, nil
}

func featuresForVersion(version string) (Feature, error) {
	v, err := utilversion.Parse(version)
	if err != nil {
		return 0, fmt.Errorf("could not parse version %s: %w", version, err)
	}
	features := Feature(0)
	if v.LessThan(utilversion.MustParse("2.0")) {
		features.Set(FeatureConfigCheck)
	}
	if v.AtLeast(utilversion.MustParse("2.1")) {
		features.Set(FeatureContentEvent)
	}
	return features, nil
}

func verifyContainerd(ctx context.Context, client *client.Client, features Feature, registryConfigPath string) error {
	log := logr.FromContextOrDiscard(ctx)
	ok, err := client.IsServing(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("could not reach Containerd service")
	}

	if !features.Has(FeatureConfigCheck) {
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
	err = verifyStatusResponse(statusResp, registryConfigPath)
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

func contentLabelsToReferences(l map[string]string, dgst digest.Digest) ([]Reference, error) {
	refs := []Reference{}
	for k, v := range l {
		if !strings.HasPrefix(k, labels.LabelDistributionSource) {
			continue
		}
		ref := Reference{
			Registry:   strings.TrimPrefix(k, labels.LabelDistributionSource+"."),
			Repository: v,
			Digest:     dgst,
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("no distribution source labels found for %s", dgst)
	}
	return refs, nil
}

// Refer to containerd registry configuration documentation for more information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, configPath string, mirroredRegistries, mirrorTargets []string, resolveTags, prependExisting bool, username, password string) error {
	log := logr.FromContextOrDiscard(ctx)

	// Parse and verify mirror urls.
	parsedMirroredRegistries, err := parseRegistries(mirroredRegistries, true)
	if err != nil {
		return err
	}
	parsedMirrorTargets, err := parseRegistries(mirrorTargets, false)
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
	if parsedMirrorRegistry == wildcardRegistryURL {
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
