package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/opencontainers/go-digest"
)

type referenceKind string

const (
	referenceKindManifest = "Manifest"
	referenceKindBlob     = "Blob"
)

type reference struct {
	kind             referenceKind
	name             string
	dgst             digest.Digest
	originalRegistry string
}

func (r reference) hasLatestTag() bool {
	if r.name == "" {
		return false
	}
	_, tag, _ := strings.Cut(r.name, ":")
	return tag == "latest"
}

// Package is used to parse components from requests which comform with the OCI distribution spec.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md
// /v2/<name>/manifests/<reference>
// /v2/<name>/blobs/<reference>

var (
	nameRegex           = regexp.MustCompile(`([a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*)`)
	tagRegex            = regexp.MustCompile(`([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})`)
	manifestRegexTag    = regexp.MustCompile(`/v2/` + nameRegex.String() + `/manifests/` + tagRegex.String() + `$`)
	manifestRegexDigest = regexp.MustCompile(`/v2/` + nameRegex.String() + `/manifests/(.*)`)
	blobsRegexDigest    = regexp.MustCompile(`/v2/` + nameRegex.String() + `/blobs/(.*)`)
)

func parsePathComponents(originalRegistry, path string) (reference, error) {
	comps := manifestRegexTag.FindStringSubmatch(path)
	if len(comps) == 6 {
		if originalRegistry == "" {
			return reference{}, errors.New("registry parameter needs to be set for tag references")
		}
		name := fmt.Sprintf("%s/%s:%s", originalRegistry, comps[1], comps[5])
		ref := reference{
			kind:             referenceKindManifest,
			name:             name,
			originalRegistry: originalRegistry,
		}
		return ref, nil
	}
	comps = manifestRegexDigest.FindStringSubmatch(path)
	if len(comps) == 6 {
		ref := reference{
			kind:             referenceKindManifest,
			dgst:             digest.Digest(comps[5]),
			originalRegistry: originalRegistry,
		}
		return ref, nil
	}
	comps = blobsRegexDigest.FindStringSubmatch(path)
	if len(comps) == 6 {
		ref := reference{
			kind:             referenceKindBlob,
			dgst:             digest.Digest(comps[5]),
			originalRegistry: originalRegistry,
		}
		return ref, nil
	}
	return reference{}, errors.New("distribution path could not be parsed")
}
