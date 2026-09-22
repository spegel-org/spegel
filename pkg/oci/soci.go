package oci

// SOCI (Seekable OCI) media types for AWS SOCI snapshotter support.
// Reference: https://github.com/awslabs/soci-snapshotter
//
// AWS SOCI has two versions:
// - V1: Original format, being deprecated (until February 2026)
// - V2: New format with improved index management, enabled by default
const (
	// MediaTypeSOCIIndexV1 is the media type for SOCI V1 index artifacts.
	MediaTypeSOCIIndexV1 = "application/vnd.amazon.soci.index.v1+json"

	// MediaTypeSOCIIndexV2 is the media type for SOCI V2 index artifacts.
	MediaTypeSOCIIndexV2 = "application/vnd.amazon.soci.index.v2+json"

	// MediaTypeSOCIzTOC is the media type for SOCI zTOC (ztoc table of contents) artifacts.
	MediaTypeSOCIzTOC = "application/vnd.amazon.soci.ztoc.v1+json"

	// MediaTypeSOCILayer is the media type for SOCI layer artifacts.
	MediaTypeSOCILayer = "application/vnd.amazon.soci.layer.v1.tar+gzip"
)

// IsSOCIMediaType returns true if the given media type is a SOCI-related media type.
func IsSOCIMediaType(mt string) bool {
	switch mt {
	case MediaTypeSOCIIndexV1, MediaTypeSOCIIndexV2, MediaTypeSOCIzTOC, MediaTypeSOCILayer:
		return true
	default:
		return false
	}
}

// IsSOCIIndexMediaType returns true if the given media type is a SOCI index (V1 or V2).
func IsSOCIIndexMediaType(mt string) bool {
	return mt == MediaTypeSOCIIndexV1 || mt == MediaTypeSOCIIndexV2
}
