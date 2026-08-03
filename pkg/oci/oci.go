package oci

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/containerd/containerd/v2/core/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/httpx"
)

const (
	// Most registries do not accept manifests larger than 4MB.
	// https://github.com/opencontainers/distribution-spec/blob/main/spec.md#pushing-manifests
	ManifestMaxSize = 4 * 1024 * 1024
)

// FingerprintMediaType attempts to determine the media type based on the json structure.
func FingerprintMediaType(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	//nolint:errcheck // We are only interested in the error type not the error content.
	if _, ok := errors.AsType[*json.SyntaxError](err); ok {
		return httpx.ContentTypeBinary, nil
	}
	if err != nil {
		return "", err
	}
	if tok != json.Delim('{') {
		return "", errors.New("expected object start")
	}

	if !dec.More() {
		b, err := io.ReadAll(dec.Buffered())
		if err != nil {
			return "", err
		}
		if len(b)+int(dec.InputOffset()) == 2 {
			return ocispec.MediaTypeEmptyJSON, nil
		}
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

func IsManifestsMediatype(mt string) bool {
	switch mt {
	case ocispec.MediaTypeImageIndex,
		ocispec.MediaTypeImageManifest,
		images.MediaTypeDockerSchema2ManifestList,
		images.MediaTypeDockerSchema2Manifest:
		return true
	default:
		return false
	}
}
