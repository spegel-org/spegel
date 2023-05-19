package oci

import (
	"regexp"

	"github.com/opencontainers/go-digest"
)

// Package is used to parse components from requests which comform with the OCI distribution spec.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md

var (
	nameRegex           = regexp.MustCompile(`([a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*)`)
	tagRegex            = regexp.MustCompile(`([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})`)
	manifestRegexTag    = regexp.MustCompile(`/v2/` + nameRegex.String() + `/manifests/` + tagRegex.String() + `$`)
	manifestRegexDigest = regexp.MustCompile(`/v2/` + nameRegex.String() + `/manifests/(.*)`)
	blobsRegexDigest    = regexp.MustCompile(`/v2/` + nameRegex.String() + `/blobs/(.*)`)
)

func manifestWithTagReference(registry, path string) (Image, bool, error) {
	comps := manifestRegexTag.FindStringSubmatch(path)
	if len(comps) != 6 {
		return Image{}, false, nil
	}
	img := NewImageWithTag(registry, comps[1], comps[5])
	return img, true, nil
}

func manifestWithDigestReference(registry, path string) (Image, bool, error) {
	comps := manifestRegexDigest.FindStringSubmatch(path)
	if len(comps) != 6 {
		return Image{}, false, nil
	}
	img := NewImageWithDigest(registry, comps[1], digest.Digest(comps[5]))
	return img, true, nil
}

// ManifestReference parses name and reference components from manifest path and returns an image reference.
// If path does not match any of the regex patterns false will be returned without an error.
// /v2/<name>/manifests/<reference>
func ManifestReference(registry, path string) (Image, bool, error) {
	img, ok, err := manifestWithTagReference(registry, path)
	if err != nil {
		return Image{}, ok, err
	}
	if ok {
		return img, ok, nil
	}
	img, ok, err = manifestWithDigestReference(registry, path)
	if err != nil {
		return Image{}, ok, err
	}
	if ok {
		return img, ok, nil
	}
	return Image{}, false, nil
}

// BlobReference parses name and reference components from blob path and returns and image reference.
// If path does not match the regex pattern false will be returned without an error.
// /v2/<name>/blobs/<reference>
func BlobReference(registry, path string) (Image, bool, error) {
	comps := blobsRegexDigest.FindStringSubmatch(path)
	if len(comps) != 6 {
		return Image{}, false, nil
	}
	img := NewImageWithDigest(registry, comps[1], digest.Digest(comps[5]))
	return img, true, nil
}

// Any reference returns the name and tag or digest for a path whcih matches any of the request paths.
func AnyReference(registry, path string) (Image, bool, error) {
	img, ok, err := ManifestReference(registry, path)
	if err != nil {
		return Image{}, ok, err
	}
	if ok {
		return img, ok, nil
	}
	img, ok, err = BlobReference(registry, path)
	if err != nil {
		return Image{}, ok, err
	}
	if ok {
		return img, ok, nil
	}
	return Image{}, false, nil
}
