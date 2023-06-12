package header

import (
	"fmt"
	"net/http"
	"net/url"
)

const (
	RegistryHeader = "X-Spegel-Registry"
	MirrorHeader   = "X-Spegel-Mirror"
	ExternalHeader = "X-Spegel-External"
)

// GetRemoteRegistry returns the target registry passed in the header.
func GetRemoteRegistry(header http.Header) (string, error) {
	registry := header.Get(RegistryHeader)
	if registry == "" {
		return "", fmt.Errorf("registry header cannot be empty")
	}
	registryUrl, err := url.Parse(registry)
	if err != nil {
		return "", fmt.Errorf("could not parse registry value: %w", err)
	}
	return registryUrl.Host, nil
}

// IsMirrorRequest returns true if mirror header is present.
func IsMirrorRequest(header http.Header) bool {
	mirror := header.Get(MirrorHeader)
	return mirror == "true"
}

// IsExternalRequest returns true if external header is present.
func IsExternalRequest(header http.Header) bool {
	external := header.Get(ExternalHeader)
	return external == "true"
}
