package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrNotFound = errors.New("content not found")
)

type EventType string

const (
	CreateEvent EventType = "CREATE"
	DeleteEvent EventType = "DELETE"
)

type OCIEvent struct {
	Type EventType
	Key  string
}

type Content struct {
	Digest     digest.Digest
	Registires []string
}

type Store interface {
	// Name returns the name of the store implementation.
	Name() string

	// Verify checks that all expected configuration is set.
	Verify(ctx context.Context) error

	// Subscribe will notify for any image events ocuring in the store backend.
	Subscribe(ctx context.Context) (<-chan OCIEvent, error)

	// ListImages returns a list of all local images.
	ListImages(ctx context.Context) ([]Image, error)

	// Resolve returns the digest for the tagged image name reference.
	// The ref is expected to be in the format `registry/name:tag`.
	Resolve(ctx context.Context, ref string) (digest.Digest, error)

	// ListContents returns a list of all the contents.
	ListContents(ctx context.Context) ([]Content, error)

	// Size returns the content byte size for the given digest.
	// Will return ErrNotFound if the digest cannot be found.
	Size(ctx context.Context, dgst digest.Digest) (int64, error)

	// GetManifest returns the manifest content for the given digest.
	// Will return ErrNotFound if the digest cannot be found.
	GetManifest(ctx context.Context, dgst digest.Digest) ([]byte, string, error)

	// GetBlob returns a stream of the blob content for the given digest.
	// Will return ErrNotFound if the digest cannot be found.
	GetBlob(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error)
}

type UnknownDocument struct {
	MediaType string `json:"mediaType"`
	specs.Versioned
}

func DetermineMediaType(b []byte) (string, error) {
	var ud UnknownDocument
	if err := json.Unmarshal(b, &ud); err != nil {
		return "", err
	}
	if ud.SchemaVersion == 2 && ud.MediaType != "" {
		return ud.MediaType, nil
	}
	data := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &data); err != nil {
		return "", err
	}
	_, architectureOk := data["architecture"]
	_, osOk := data["os"]
	_, rootfsOk := data["rootfs"]
	if architectureOk && osOk && rootfsOk {
		return ocispec.MediaTypeImageConfig, nil
	}
	_, manifestsOk := data["manifests"]
	if ud.SchemaVersion == 2 && manifestsOk {
		return ocispec.MediaTypeImageIndex, nil
	}
	_, configOk := data["config"]
	if ud.SchemaVersion == 2 && configOk {
		return ocispec.MediaTypeImageManifest, nil
	}
	return "", errors.New("not able to determine media type")
}

func WalkImage(ctx context.Context, store Store, img Image) ([]digest.Digest, error) {
	dgsts := []digest.Digest{}
	err := walk(ctx, []digest.Digest{img.Digest}, func(dgst digest.Digest) ([]digest.Digest, error) {
		b, mt, err := store.GetManifest(ctx, dgst)
		if err != nil {
			return nil, err
		}
		dgsts = append(dgsts, dgst)
		switch mt {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			var idx ocispec.Index
			if err := json.Unmarshal(b, &idx); err != nil {
				return nil, err
			}
			manifestDgsts := []digest.Digest{}
			for _, m := range idx.Manifests {
				_, err := store.Size(ctx, m.Digest)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				manifestDgsts = append(manifestDgsts, m.Digest)
			}
			if len(manifestDgsts) == 0 {
				return nil, fmt.Errorf("could not find any platforms with local content in manifest %s", dgst)
			}
			return manifestDgsts, nil
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			var manifest ocispec.Manifest
			err := json.Unmarshal(b, &manifest)
			if err != nil {
				return nil, err
			}
			dgsts = append(dgsts, manifest.Config.Digest)
			for _, layer := range manifest.Layers {
				dgsts = append(dgsts, layer.Digest)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected media type %s for digest %s", mt, dgst)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk image manifests: %w", err)
	}
	if len(dgsts) == 0 {
		return nil, errors.New("no image digests found")
	}
	return dgsts, nil
}

func walk(ctx context.Context, dgsts []digest.Digest, handler func(dgst digest.Digest) ([]digest.Digest, error)) error {
	for _, dgst := range dgsts {
		children, err := handler(dgst)
		if err != nil {
			return err
		}
		if len(children) == 0 {
			continue
		}
		err = walk(ctx, children, handler)
		if err != nil {
			return err
		}
	}
	return nil
}
