package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/httpx"
)

var (
	nameRegexStr        = `([a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*)`
	tagRegexStr         = `([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})`
	nameRegex           = regexp.MustCompile(`^` + nameRegexStr + `$`)
	tagRegex            = regexp.MustCompile(`^` + tagRegexStr + `$`)
	manifestRegexTag    = regexp.MustCompile(`/v2/` + nameRegexStr + `/manifests/` + tagRegexStr + `$`)
	manifestRegexDigest = regexp.MustCompile(`/v2/` + nameRegexStr + `/manifests/(.*)`)
	blobsRegexDigest    = regexp.MustCompile(`/v2/` + nameRegexStr + `/blobs/(.*)`)
)

// DistributionKind represents the kind of content.
type DistributionKind string

const (
	DistributionKindManifest = "manifests"
	DistributionKindBlob     = "blobs"
)

// DistributionPath contains the individual parameters from a OCI distribution spec request.
type DistributionPath struct {
	Kind     DistributionKind
	Name     string
	Digest   digest.Digest
	Tag      string
	Registry string
}

// Reference returns the digest if set or alternatively if not the full image reference with the tag.
func (d DistributionPath) Reference() string {
	if d.Digest != "" {
		return d.Digest.String()
	}
	return fmt.Sprintf("%s/%s:%s", d.Registry, d.Name, d.Tag)
}

// IsLatestTag returns true if the tag has the value latest.
func (d DistributionPath) IsLatestTag() bool {
	return d.Tag == "latest"
}

// URL returns the reconstructed URL containing the path and query parameters.
func (d DistributionPath) URL() *url.URL {
	ref := d.Digest.String()
	if ref == "" {
		ref = d.Tag
	}
	return &url.URL{
		Scheme:   "https",
		Host:     d.Registry,
		Path:     fmt.Sprintf("/v2/%s/%s/%s", d.Name, d.Kind, ref),
		RawQuery: fmt.Sprintf("ns=%s", d.Registry),
	}
}

// ParseDistributionPath gets the parameters from a URL which conforms with the OCI distribution spec.
// It returns a distribution path which contains all the individual parameters.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md
func ParseDistributionPath(u *url.URL) (DistributionPath, error) {
	registry := u.Query().Get("ns")
	comps := manifestRegexTag.FindStringSubmatch(u.Path)
	if len(comps) == 6 {
		if registry == "" {
			return DistributionPath{}, errors.New("registry parameter needs to be set for tag references")
		}
		dist := DistributionPath{
			Kind:     DistributionKindManifest,
			Name:     comps[1],
			Tag:      comps[5],
			Registry: registry,
		}
		return dist, nil
	}
	comps = manifestRegexDigest.FindStringSubmatch(u.Path)
	if len(comps) == 6 {
		dgst, err := digest.Parse(comps[5])
		if err != nil {
			return DistributionPath{}, err
		}
		dist := DistributionPath{
			Kind:     DistributionKindManifest,
			Name:     comps[1],
			Digest:   dgst,
			Registry: registry,
		}
		return dist, nil
	}
	comps = blobsRegexDigest.FindStringSubmatch(u.Path)
	if len(comps) == 6 {
		dgst, err := digest.Parse(comps[5])
		if err != nil {
			return DistributionPath{}, err
		}
		dist := DistributionPath{
			Kind:     DistributionKindBlob,
			Name:     comps[1],
			Digest:   dgst,
			Registry: registry,
		}
		return dist, nil
	}
	return DistributionPath{}, errors.New("distribution path could not be parsed")
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

func (e *DistributionError) ResponseBody() ([]byte, error) {
	errResp := struct {
		Errors []DistributionError `json:"errors"`
	}{
		Errors: []DistributionError{*e},
	}
	b, err := json.Marshal(errResp)
	if err != nil {
		return nil, err
	}
	return b, nil
}
