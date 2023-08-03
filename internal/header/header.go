package header

import (
	"net/http"
)

const (
	ExternalHeader = "X-Spegel-External"
)

// IsExternalRequest returns true if external header is present.
func IsExternalRequest(header http.Header) bool {
	external := header.Get(ExternalHeader)
	return external == "true"
}
