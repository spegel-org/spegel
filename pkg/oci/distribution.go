package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/httpx"
)

var (
	repoRegexStr        = `([a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*)`
	tagRegexStr         = `([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})`
	repoRegex           = regexp.MustCompile(`^` + repoRegexStr + `$`)
	tagRegex            = regexp.MustCompile(`^` + tagRegexStr + `$`)
	manifestRegexTag    = regexp.MustCompile(`/v2/` + repoRegexStr + `/manifests/` + tagRegexStr + `$`)
	manifestRegexDigest = regexp.MustCompile(`/v2/` + repoRegexStr + `/manifests/(.*)`)
	blobsRegexDigest    = regexp.MustCompile(`/v2/` + repoRegexStr + `/blobs/(.*)`)
)

// DistributionKind represents the kind of content.
type DistributionKind string

const (
	DistributionKindManifest = "manifests"
	DistributionKindBlob     = "blobs"
)

// DistributionPath contains the individual parameters from a OCI distribution spec request.
type DistributionPath struct {
	Reference
	Kind DistributionKind
}

func NewDistributionPath(ref Reference, kind DistributionKind) (DistributionPath, error) {
	if err := ref.Validate(); err != nil {
		return DistributionPath{}, err
	}
	if ref.Tag != "" && ref.Digest != "" {
		return DistributionPath{}, errors.New("tag and digest cant both be set")
	}
	if kind == DistributionKindBlob && ref.Tag != "" {
		return DistributionPath{}, errors.New("tag reference cannot be used for blobs")
	}
	dist := DistributionPath{
		Kind:      kind,
		Reference: ref,
	}
	return dist, nil
}

func (d DistributionPath) String() string {
	return d.URL().String()
}

// URL returns the reconstructed URL containing the path and query parameters.
func (d DistributionPath) URL() *url.URL {
	ref := d.Digest.String()
	if ref == "" {
		ref = d.Tag
	}
	u := &url.URL{
		Scheme: "https",
		Host:   d.Registry,
		Path:   fmt.Sprintf("/v2/%s/%s/%s", d.Repository, d.Kind, ref),
	}
	// Only add ns parameter if registry is set (containerd mode)
	// CRI-O mode doesn't use ns parameter
	if d.Registry != "" {
		u.RawQuery = fmt.Sprintf("ns=%s", d.Registry)
	}
	return u
}

// ParseDistributionPath gets the parameters from a URL which conforms with the OCI distribution spec.
// It returns a distribution path which contains all the individual parameters.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md
//
// Supports two formats:
// 1. Containerd: /v2/<repository>/manifests/<ref>?ns=<registry>
// 2. CRI-O: /v2/<registry>/<repository>/manifests/<ref> (registry embedded in path)
func ParseDistributionPath(u *url.URL) (DistributionPath, error) {
	registry := u.Query().Get("ns")
	comps := manifestRegexTag.FindStringSubmatch(u.Path)
	if len(comps) == 3 {
		repo := comps[1]
		tag := comps[2]

		// If no ns parameter, try to extract registry from repository path (CRI-O format)
		if registry == "" {
			registry, repo = extractRegistryFromRepo(repo)
			if registry == "" {
				return DistributionPath{}, errors.New("registry parameter needs to be set for tag references")
			}
		}
		ref := Reference{
			Registry:   registry,
			Repository: repo,
			Tag:        tag,
		}
		dist, err := NewDistributionPath(ref, DistributionKindManifest)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	comps = manifestRegexDigest.FindStringSubmatch(u.Path)
	if len(comps) == 3 {
		repo := comps[1]
		dgst, err := digest.Parse(comps[2])
		if err != nil {
			return DistributionPath{}, err
		}

		// If no ns parameter, try to extract registry from repository path (CRI-O format)
		if registry == "" {
			registry, repo = extractRegistryFromRepo(repo)
		}
		ref := Reference{
			Registry:   registry,
			Repository: repo,
			Digest:     dgst,
		}
		dist, err := NewDistributionPath(ref, DistributionKindManifest)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	comps = blobsRegexDigest.FindStringSubmatch(u.Path)
	if len(comps) == 3 {
		repo := comps[1]
		dgst, err := digest.Parse(comps[2])
		if err != nil {
			return DistributionPath{}, err
		}

		// If no ns parameter, try to extract registry from repository path (CRI-O format)
		if registry == "" {
			registry, repo = extractRegistryFromRepo(repo)
		}
		ref := Reference{
			Registry:   registry,
			Repository: repo,
			Digest:     dgst,
		}
		dist, err := NewDistributionPath(ref, DistributionKindBlob)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	return DistributionPath{}, errors.New("distribution path could not be parsed")
}

// extractRegistryFromRepo handles CRI-O's format where the registry is embedded in the path.
// For example: "docker.io/library/redis" -> registry="docker.io", repo="library/redis"
// Returns empty registry if the first component doesn't look like a registry hostname.
func extractRegistryFromRepo(fullRepo string) (registry, repo string) {
	idx := strings.Index(fullRepo, "/")
	if idx == -1 {
		return "", fullRepo
	}

	firstComponent := fullRepo[:idx]
	remainder := fullRepo[idx+1:]

	// Check if the first component looks like a registry hostname
	// (contains a dot or colon, or is "localhost")
	if strings.Contains(firstComponent, ".") ||
		strings.Contains(firstComponent, ":") ||
		firstComponent == "localhost" {
		return firstComponent, remainder
	}

	return "", fullRepo
}

var _ httpx.ResponseError = &DistributionError{}

type DistributionErrorCode string

const (
	ErrCodeBlobUnknown         DistributionErrorCode = "BLOB_UNKNOWN"
	ErrCodeBlobUploadInvalid   DistributionErrorCode = "BLOB_UPLOAD_INVALID"
	ErrCodeBlobUploadUnknown   DistributionErrorCode = "BLOB_UPLOAD_UNKNOWN"
	ErrCodeDigestInvalid       DistributionErrorCode = "DIGEST_INVALID"
	ErrCodeManifestBlobUnknown DistributionErrorCode = "MANIFEST_BLOB_UNKNOWN"
	ErrCodeManifestInvalid     DistributionErrorCode = "MANIFEST_INVALID"
	ErrCodeManifestUnknown     DistributionErrorCode = "MANIFEST_UNKNOWN"
	ErrCodeNameInvalid         DistributionErrorCode = "NAME_INVALID"
	ErrCodeNameUnknown         DistributionErrorCode = "NAME_UNKNOWN"
	ErrCodeSizeInvalid         DistributionErrorCode = "SIZE_INVALID"
	ErrCodeUnauthorized        DistributionErrorCode = "UNAUTHORIZED"
	ErrCodeDenied              DistributionErrorCode = "DENIED"
	ErrCodeUnsupported         DistributionErrorCode = "UNSUPPORTED"
	ErrCodeTooManyRequests     DistributionErrorCode = "TOOMANYREQUESTS"
)

type DistributionError struct {
	Code    DistributionErrorCode `json:"code"`
	Detail  any                   `json:"detail,omitempty"`
	Message string                `json:"message,omitempty"`
}

func NewDistributionError(code DistributionErrorCode, message string, detail any) *DistributionError {
	return &DistributionError{
		Code:    code,
		Message: message,
		Detail:  detail,
	}
}

func (e *DistributionError) Error() string {
	return fmt.Sprintf("%s %s", e.Code, e.Message)
}

func (e *DistributionError) ResponseBody() ([]byte, string, error) {
	errResp := struct {
		Errors []DistributionError `json:"errors"`
	}{
		Errors: []DistributionError{*e},
	}
	b, err := json.Marshal(errResp)
	if err != nil {
		return nil, "", err
	}
	return b, httpx.ContentTypeJSON, nil
}
