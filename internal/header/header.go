package header

import (
	"net/http"
)

const (
	MirrorHeader   = "X-Spegel-Mirror"
	ExternalHeader = "X-Spegel-External"
)

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
