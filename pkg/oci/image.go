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
	Name       string
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

type EventType string

const (
	CreateEvent EventType = "CREATE"
	UpdateEvent EventType = "UPDATE"
	DeleteEvent EventType = "DELETE"
)

type ImageEvent struct {
	Image Image
	Type  EventType
}

func NewImage(name, registry, repository, tag string, dgst digest.Digest) (Image, error) {
	if name == "" {
		return Image{}, errors.New("image needs to contain a name")
	}
	if registry == "" {
		return Image{}, errors.New("image needs to contain a registry")
	}
	if repository == "" {
		return Image{}, errors.New("image needs to repository a digest")
	}
	if dgst == "" {
		return Image{}, fmt.Errorf("image needs to contain a digest, image %s, registry %s, repository %s, tag %s", name, registry, repository, tag)
	}
	return Image{
		Name:       name,
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
		tag = fmt.Sprintf(":%s", i.Tag)
	}
	return fmt.Sprintf("%s/%s%s@%s", i.Registry, i.Repository, tag, i.Digest.String())
}

func (i Image) TagName() (string, bool) {
	if i.Tag == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s:%s", i.Registry, i.Repository, i.Tag), true
}

var splitRe = regexp.MustCompile(`[:@]`)

func Parse(s string, extraDgst digest.Digest) (Image, error) {
	if strings.Contains(s, "://") {
		return Image{}, fmt.Errorf("invalid reference: %v", s)
	}
	u, err := url.Parse("dummy://" + s)
	if err != nil {
		return Image{}, err
	}
	if u.Scheme != "dummy" {
		return Image{}, fmt.Errorf("invalid reference: %v", u.Scheme)
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

	if dgst == "" {
		dgst = extraDgst
	}
	if extraDgst != "" && dgst != extraDgst {
		return Image{}, fmt.Errorf("invalid digest set does not match parsed digest: %v %v", s, dgst)
	}
	img, err := NewImage(s, u.Host, repository, tag, dgst)
	if err != nil {
		return Image{}, err
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
