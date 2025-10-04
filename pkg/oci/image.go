package oci

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	digest "github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/internal/option"
)

const (
	DefaultRegistry  = "docker.io"
	DefaultNamespace = "library"
	DefaultTag       = "latest"
)

type Image struct {
	Reference
}

func NewImage(registry, repository, tag string, dgst digest.Digest) (Image, error) {
	ref := Reference{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Digest:     dgst,
	}
	if err := ref.Validate(); err != nil {
		return Image{}, err
	}
	return Image{
		Reference: ref,
	}, nil
}

func (i Image) String() string {
	tag := ""
	if i.Tag != "" {
		tag = ":" + i.Tag
	}
	digest := ""
	if i.Digest != "" {
		digest = "@" + i.Digest.String()
	}
	return fmt.Sprintf("%s/%s%s%s", i.Registry, i.Repository, tag, digest)
}

// TagName returns the full tag reference string if tag is set.
func (i Image) TagName() (string, bool) {
	if i.Tag == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s:%s", i.Registry, i.Repository, i.Tag), true
}

// DistributionPath returns the distribution path for the images top layer.
func (i Image) DistributionPath() DistributionPath {
	ref := i.Reference
	if ref.Digest != "" {
		ref.Tag = ""
	}
	return DistributionPath{
		Reference: ref,
		Kind:      DistributionKindManifest,
	}
}

type ParseImageConfig struct {
	Digest        digest.Digest
	RequireDigest bool
	Strict        bool
}

type ParseImageOption = option.Option[ParseImageConfig]

// WithDigest adds an additional digest outside of the parsed string.
func WithDigest(dgst digest.Digest) ParseImageOption {
	return func(cfg *ParseImageConfig) error {
		cfg.Digest = dgst
		return nil
	}
}

// AllowTagOnly disables enforcement of digest in parsed image.
func AllowTagOnly() ParseImageOption {
	return func(cfg *ParseImageConfig) error {
		cfg.RequireDigest = false
		return nil
	}
}

// AllowDefaults disables strict validation of image references and appends defaults.
func AllowDefaults() ParseImageOption {
	return func(cfg *ParseImageConfig) error {
		cfg.Strict = false
		return nil
	}
}

// ParseImage parses the image reference.
func ParseImage(s string, opts ...ParseImageOption) (Image, error) {
	cfg := ParseImageConfig{
		RequireDigest: true,
		Strict:        true,
	}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return Image{}, err
	}

	registry, repository, tag, dgst, err := parseImage(s)
	if err != nil {
		return Image{}, err
	}
	if cfg.Digest != "" {
		if dgst != "" && dgst != cfg.Digest {
			return Image{}, fmt.Errorf("invalid digest set does not match parsed digest: %v %v", s, dgst)
		}
		dgst = cfg.Digest
	}
	if cfg.RequireDigest {
		if dgst == "" {
			return Image{}, errors.New("image needs to contain a digest")
		}
	}
	if !cfg.Strict {
		if registry == "" {
			registry = DefaultRegistry
		}
		if len(strings.Split(repository, "/")) == 1 && registry == DefaultRegistry {
			repository = DefaultNamespace + "/" + repository
		}
		if tag == "" {
			tag = DefaultTag
		}
	}
	img, err := NewImage(registry, repository, tag, dgst)
	if err != nil {
		return Image{}, err
	}
	return img, nil
}

func parseImage(s string) (string, string, string, digest.Digest, error) {
	if strings.Contains(s, "://") {
		return "", "", "", "", errors.New("invalid reference format")
	}
	comps := strings.Split(s, "/")
	if len(comps) == 0 {
		return "", "", "", "", errors.New("invalid reference format")
	}

	var registry string
	if len(comps) > 1 {
		if isRegistry(comps[0]) {
			registry = comps[0]
			comps = comps[1:]
		}
	}

	last := comps[len(comps)-1]
	_, dgstStr, ok := strings.Cut(last, "@")
	var dgst digest.Digest
	if ok {
		var err error
		dgst, err = digest.Parse(dgstStr)
		if err != nil {
			return "", "", "", "", err
		}
		last = strings.TrimSuffix(last, "@"+dgstStr)
	}
	_, tag, ok := strings.Cut(last, ":")
	if ok {
		if !tagRegex.MatchString(tag) {
			return "", "", "", "", fmt.Errorf("tag %s is invalid", tag)
		}
		last = strings.TrimSuffix(last, ":"+tag)
	}
	comps[len(comps)-1] = last

	repository := strings.Join(comps, "/")
	if !nameRegex.MatchString(repository) {
		return "", "", "", "", fmt.Errorf("repository %s is invalid", repository)
	}

	return registry, repository, tag, dgst, nil
}

func isRegistry(s string) bool {
	if u, err := url.Parse("//" + s); err != nil || u.Host != s {
		return false
	}
	return strings.ContainsRune(s, '.') || strings.ContainsRune(s, ':')
}
