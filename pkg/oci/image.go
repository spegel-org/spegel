package oci

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

type Image struct {
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

func NewImage(registry, repository, tag string, dgst digest.Digest) (Image, error) {
	if registry == "" {
		return Image{}, errors.New("image needs to contain a registry")
	}
	if repository == "" {
		return Image{}, errors.New("image needs to contain a repository")
	}
	if dgst != "" {
		if err := dgst.Validate(); err != nil {
			return Image{}, err
		}
	}
	if dgst == "" && tag == "" {
		return Image{}, errors.New("either tag or digest has to be set")
	}
	return Image{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Digest:     dgst,
	}, nil
}

func (i Image) IsLatestTag() bool {
	return i.Tag == "latest"
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

func (i Image) TagName() (string, bool) {
	if i.Tag == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s:%s", i.Registry, i.Repository, i.Tag), true
}

var splitRe = regexp.MustCompile(`[:@]`)

type ParseImageConfig struct {
	Digest digest.Digest
	Strict bool
}

func (cfg *ParseImageConfig) Apply(opts ...ParseImageOption) error {
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

type ParseImageOption func(cfg *ParseImageConfig) error

// Enforces the presence of digest in the image.
func WithStrict() ParseImageOption {
	return func(cfg *ParseImageConfig) error {
		cfg.Strict = true
		return nil
	}
}

// Adds an additional digest outside of the parsed string.
func WithDigest(dgst digest.Digest) ParseImageOption {
	return func(cfg *ParseImageConfig) error {
		cfg.Digest = dgst
		return nil
	}
}

// ParseImage parses the image reference.
func ParseImage(s string, opts ...ParseImageOption) (Image, error) {
	cfg := ParseImageConfig{}
	err := cfg.Apply(opts...)
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
	if cfg.Strict {
		if dgst == "" {
			return Image{}, errors.New("image needs to contain a digest")
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
		return "", "", "", "", errors.New("invalid reference")
	}
	u, err := url.Parse("dummy://" + s)
	if err != nil {
		return "", "", "", "", err
	}
	if u.Scheme != "dummy" {
		return "", "", "", "", errors.New("invalid reference")
	}
	if u.Host == "" {
		return "", "", "", "", errors.New("hostname required")
	}
	var object string
	if idx := splitRe.FindStringIndex(u.Path); idx != nil {
		// This allows us to retain the @ to signify digests or shortened digests in
		// the object.
		object = u.Path[idx[0]:]
		if object[:1] == ":" {
			object = object[1:]
		}
		u.Path = u.Path[:idx[0]]
	}
	tag, dgst := splitTagAndDigest(object)
	tag, _, _ = strings.Cut(tag, "@")
	repository := strings.TrimPrefix(u.Path, "/")
	return u.Host, repository, tag, dgst, nil
}

func splitTagAndDigest(obj string) (tag string, dgst digest.Digest) {
	parts := strings.SplitAfterN(obj, "@", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], digest.Digest(parts[1])
}
