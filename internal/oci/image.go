package oci

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

type Image struct {
	Name       string
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

func NewImageWithTag(registry, repository, tag string) Image {
	name := fmt.Sprintf("%s/%s:%s", registry, repository, tag)
	return Image{
		Name:       name,
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
	}
}

func NewImageWithDigest(registry, repository string, dgst digest.Digest) Image {
	name := fmt.Sprintf("%s/%s@%s", registry, repository, dgst.String())
	return Image{
		Name:       name,
		Registry:   registry,
		Repository: repository,
		Digest:     dgst,
	}
}

func (i Image) Key() string {
	if i.Digest.String() != "" {
		return i.Digest.String()
	}
	return i.Name
}

func (i Image) TagReference() (string, bool) {
	if i.Tag == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s:%s", i.Registry, i.Repository, i.Tag), true
}

var splitRe = regexp.MustCompile(`[:@]`)

func ParseWithDigest(s string, dgst digest.Digest) (Image, error) {
	img, err := Parse(s)
	if err != nil {
		return Image{}, err
	}
	if img.Digest == "" {
		img.Digest = dgst
	}
	if img.Digest != dgst {
		return Image{}, fmt.Errorf("invalid digest set does not match parsed digest: %v %v", s, dgst)
	}
	return img, nil
}

func Parse(s string) (Image, error) {
	if strings.Contains(s, "://") {
		return Image{}, fmt.Errorf("invalid reference")
	}
	u, err := url.Parse("dummy://" + s)
	if err != nil {
		return Image{}, err
	}
	if u.Scheme != "dummy" {
		return Image{}, fmt.Errorf("invalid reference")
	}
	if u.Host == "" {
		return Image{}, fmt.Errorf("hostname required")
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
	img := Image{
		Name:       s,
		Registry:   u.Host,
		Repository: strings.TrimPrefix(u.Path, "/"),
		Tag:        tag,
		Digest:     dgst,
	}

	if img.Tag == "" && img.Digest == "" {
		return Image{}, fmt.Errorf("reference needs to contain a tag or digest")
	}
	if img.Registry == "" {
		return Image{}, fmt.Errorf("reference needs to contain a registry")
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
