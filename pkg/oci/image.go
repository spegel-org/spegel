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

func ParseImage(s string) (Image, error) {
	if strings.Contains(s, "://") {
		return Image{}, errors.New("invalid reference")
	}
	u, err := url.Parse("dummy://" + s)
	if err != nil {
		return Image{}, err
	}
	if u.Scheme != "dummy" {
		return Image{}, errors.New("invalid reference")
	}
	if u.Host == "" {
		return Image{}, errors.New("hostname required")
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
	tag, dgst := splitObject(object)
	tag, _, _ = strings.Cut(tag, "@")
	repository := strings.TrimPrefix(u.Path, "/")

	img, err := NewImage(u.Host, repository, tag, dgst)
	if err != nil {
		return Image{}, err
	}
	return img, nil
}

func ParseImageRequireDigest(s string, dgst digest.Digest) (Image, error) {
	img, err := ParseImage(s)
	if err != nil {
		return Image{}, err
	}
	if img.Digest != "" && dgst == "" {
		return img, nil
	}
	if img.Digest == "" && dgst == "" {
		return Image{}, errors.New("image needs to contain a digest")
	}
	if img.Digest == "" && dgst != "" {
		return NewImage(img.Registry, img.Repository, img.Tag, dgst)
	}
	if img.Digest != dgst {
		return Image{}, fmt.Errorf("invalid digest set does not match parsed digest: %v %v", s, img.Digest)
	}
	return img, nil
}

func splitObject(obj string) (tag string, dgst digest.Digest) {
	parts := strings.SplitAfterN(obj, "@", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], digest.Digest(parts[1])
}
