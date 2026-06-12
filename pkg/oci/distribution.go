package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"

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

	errMissingNamespace = fmt.Errorf("registry needs to be set with the ns query parameter or the %s header", HeaderNamespace)
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
	Range  *httpx.Range
	Scheme string
	Method string
	Kind   DistributionKind
}

func NewDistributionPath(ref Reference, kind DistributionKind, scheme, method string, rng *httpx.Range) (DistributionPath, error) {
	// Digest references are self identifying, making the registry optional.
	if ref.Registry == "" && ref.Digest != "" {
		if ref.Repository == "" {
			return DistributionPath{}, errors.New("reference needs to contain a repository")
		}
		if err := ref.Digest.Validate(); err != nil {
			return DistributionPath{}, err
		}
	} else if err := ref.Validate(); err != nil {
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
		Scheme:    scheme,
		Method:    method,
		Range:     rng,
	}
	if err := dist.Validate(); err != nil {
		return DistributionPath{}, err
	}
	return dist, nil
}

// Validate returns an error if parameter combinations are incorrect.
func (d DistributionPath) Validate() error {
	if d.Method != http.MethodHead && d.Method != http.MethodGet {
		return errors.New("fetch only supports HEAD and GET requests")
	}
	if d.Kind == DistributionKindManifest && d.Range != nil {
		return errors.New("cannot make range requests for manifests")
	}
	return nil
}

// URL returns the reconstructed URL containing the path and query parameters.
func (d DistributionPath) URL() *url.URL {
	ref := d.Digest.String()
	if ref == "" {
		ref = d.Tag
	}
	u := &url.URL{
		Scheme: d.Scheme,
		Host:   d.Registry,
		Path:   fmt.Sprintf("/v2/%s/%s/%s", d.Repository, d.Kind, ref),
	}
	if d.Registry != "" {
		u.RawQuery = fmt.Sprintf("ns=%s", d.Registry)
	}
	return u
}

// Clone returns a deep copy of the distribution path.
func (d DistributionPath) Clone() DistributionPath {
	out := d
	if d.Range != nil {
		out.Range = d.Range.Clone()
	}
	return out
}

// ParseDistributionPath gets the parameters from a URL which conforms with the OCI distribution spec.
// It returns a distribution path which contains all the individual parameters.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md
func ParseDistributionPath(req *http.Request) (DistributionPath, error) {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}

	registry, anyRegistry := requestNamespace(req)
	comps := manifestRegexTag.FindStringSubmatch(req.URL.Path)
	if len(comps) == 3 {
		if registry == "" {
			return DistributionPath{}, errors.New("registry parameter needs to be set for tag references")
		}
		ref := Reference{
			Registry:   registry,
			Repository: comps[1],
			Tag:        comps[2],
		}
		dist, err := NewDistributionPath(ref, DistributionKindManifest, scheme, req.Method, nil)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	comps = manifestRegexDigest.FindStringSubmatch(req.URL.Path)
	if len(comps) == 3 {
		dgst, err := digest.Parse(comps[2])
		if err != nil {
			return DistributionPath{}, err
		}
		if registry == "" && !anyRegistry {
			return DistributionPath{}, errMissingNamespace
		}
		ref := Reference{
			Registry:   registry,
			Repository: comps[1],
			Digest:     dgst,
		}
		dist, err := NewDistributionPath(ref, DistributionKindManifest, scheme, req.Method, nil)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	comps = blobsRegexDigest.FindStringSubmatch(req.URL.Path)
	if len(comps) == 3 {
		dgst, err := digest.Parse(comps[2])
		if err != nil {
			return DistributionPath{}, err
		}
		if registry == "" && !anyRegistry {
			return DistributionPath{}, errMissingNamespace
		}
		ref := Reference{
			Registry:   registry,
			Repository: comps[1],
			Digest:     dgst,
		}
		rng, err := httpx.ParseRangeHeader(req.Header)
		if err != nil {
			return DistributionPath{}, err
		}
		dist, err := NewDistributionPath(ref, DistributionKindBlob, scheme, req.Method, rng)
		if err != nil {
			return DistributionPath{}, err
		}
		return dist, nil
	}
	return DistributionPath{}, errors.New("distribution path could not be parsed")
}

// requestNamespace returns the registry a request is for. Mirror clients which do not
// implement the namespace query parameter, like the stargz snapshotter, can set the
// namespace with a header instead. A wildcard namespace means that the client mirrors
// any registry, which can only be served for self identifying digest references.
func requestNamespace(req *http.Request) (string, bool) {
	registry := req.URL.Query().Get("ns")
	if registry != "" {
		return registry, false
	}
	registry = req.Header.Get(HeaderNamespace)
	if slices.Contains(WildcardRegistries, registry) {
		return "", true
	}
	return registry, false
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
