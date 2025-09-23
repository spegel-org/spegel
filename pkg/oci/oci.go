package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	// Most registries do not accept manifests larger than 4MB.
	// https://github.com/opencontainers/distribution-spec/blob/main/spec.md#pushing-manifests
	ManifestMaxSize = 4 * 1024 * 1024
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

	// ListContents returns a list of all the contents.
	ListContents(ctx context.Context) ([]Content, error)

	// Resolve returns the digest for the tagged image name reference.
	// The ref is expected to be in the format `registry/name:tag`.
	Resolve(ctx context.Context, ref string) (digest.Digest, error)

	// Descriptor returns the OCI descriptor for the given digest.
	Descriptor(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, error)

	// Open returns the streamable content for the given digest.
	Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error)
}

// FingerprintMediaType attempts to determine the media type based on the json structure.
func FingerprintMediaType(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	tok, err := dec.Token()
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return httpx.ContentTypeBinary, nil
	}
	if err != nil {
		return "", err
	}
	if tok != json.Delim('{') {
		return "", errors.New("expected object start")
	}

	schemaVersion := 0
	mediaType := ""

	indexKeys := 0
	manifestKeys := 0
	configKeys := 0

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, ok := tok.(string)
		if !ok {
			return "", errors.New("unexpected token type")
		}
		switch key {
		case "schemaVersion":
			//nolint: errcheck // Allow other value types.
			dec.Decode(&schemaVersion)
		case "mediaType":
			//nolint: errcheck // Allow other value types.
			dec.Decode(&mediaType)
		// Index.
		case "manifests":
			err = dec.Decode(&[]ocispec.Descriptor{})
			if err == nil {
				indexKeys += 1
			}
		// Manifest.
		case "config":
			err = dec.Decode(&ocispec.Descriptor{})
			if err == nil {
				manifestKeys += 1
			}
		case "layers":
			err = dec.Decode(&[]ocispec.Descriptor{})
			if err == nil {
				manifestKeys += 1
			}
		// Image Config.
		case "architecture":
			var arch string
			err = dec.Decode(&arch)
			if err == nil {
				configKeys += 1
			}
		case "os":
			var os string
			err = dec.Decode(&os)
			if err == nil {
				configKeys += 1
			}
		case "rootfs":
			configKeys += 1
			var discard any
			err = dec.Decode(&discard)
			if err != nil {
				return "", err
			}
		default:
			var discard any
			err = dec.Decode(&discard)
			if err != nil {
				return "", err
			}
		}

		// Return immediately if schema version and media type is set.
		if schemaVersion == 2 && mediaType != "" {
			return mediaType, nil
		}
	}

	if indexKeys == 1 {
		return ocispec.MediaTypeImageIndex, nil
	}
	if manifestKeys == 2 {
		return ocispec.MediaTypeImageManifest, nil
	}
	if configKeys == 3 {
		return ocispec.MediaTypeImageConfig, nil
	}
	return "", errors.New("could not determine media type")
}

func WalkImage(ctx context.Context, store Store, img Image) ([]digest.Digest, error) {
	dgsts := []digest.Digest{}
	err := walk(ctx, []digest.Digest{img.Digest}, func(dgst digest.Digest) ([]digest.Digest, error) {
		desc, err := store.Descriptor(ctx, dgst)
		if err != nil {
			return nil, err
		}
		if desc.MediaType == "" {
			return nil, fmt.Errorf("descriptor media type is empty for digest %s", dgst)
		}
		dgsts = append(dgsts, dgst)
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			rc, err := store.Open(ctx, dgst)
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			decoder := json.NewDecoder(rc)
			var idx ocispec.Index
			err = decoder.Decode(&idx)
			if err != nil {
				return nil, err
			}
			manifestDgsts := []digest.Digest{}
			for _, m := range idx.Manifests {
				_, err := store.Descriptor(ctx, m.Digest)
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
			rc, err := store.Open(ctx, dgst)
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			decoder := json.NewDecoder(rc)
			var manifest ocispec.Manifest
			err = decoder.Decode(&manifest)
			if err != nil {
				return nil, err
			}
			dgsts = append(dgsts, manifest.Config.Digest)
			for _, layer := range manifest.Layers {
				dgsts = append(dgsts, layer.Digest)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected media type %s for digest %s", desc.MediaType, dgst)
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
