package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Expected unit of range and content range headers.
const RangeUnit = "bytes"

// Range represents a range header.
// Both start and end are optional to support parsing without known content size.
type Range struct {
	Start *int64 `json:"start"`
	End   *int64 `json:"end"`
}

// ParseRangeHeader parses header according to RFC 9110.
func ParseRangeHeader(header http.Header) (*Range, error) {
	h := header.Get(HeaderRange)
	if h == "" {
		return nil, nil
	}
	rangeUnitPrefix := RangeUnit + "="
	if !strings.HasPrefix(h, rangeUnitPrefix) {
		return nil, errors.New("invalid range unit")
	}
	ranges := strings.Split(strings.TrimPrefix(h, rangeUnitPrefix), ",")
	if len(ranges) != 1 {
		return nil, errors.New("multiple ranges not supported")
	}
	parts := strings.SplitN(strings.TrimSpace(ranges[0]), "-", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid range format")
	}

	rng := Range{}
	for i, part := range parts {
		if part == "" {
			continue
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, err
		}

		switch i {
		case 0:
			rng.Start = &v
		case 1:
			rng.End = &v
		}
	}
	if err := rng.Validate(); err != nil {
		return nil, err
	}
	return &rng, nil
}

// Validate checks the content of range.
func (rng Range) Validate() error {
	if rng.Start == nil && rng.End != nil && *rng.End <= 0 {
		return fmt.Errorf("suffix range %d cannot be less than one", *rng.End)
	}
	if rng.Start != nil && *rng.Start < 0 {
		return fmt.Errorf("start range %d cannot be less than zero", *rng.Start)
	}
	if rng.End != nil && *rng.End < 0 {
		return fmt.Errorf("end range %d cannot be less than zero", *rng.End)
	}
	if rng.Start != nil && rng.End != nil && *rng.Start > *rng.End {
		return fmt.Errorf("start %d cannot be larger than end %d", *rng.Start, *rng.End)
	}
	if rng.Start == nil && rng.End == nil {
		return errors.New("start and end range cannot both be empty")
	}
	return nil
}

// String returns range content as formatted header value.
func (rng Range) String() string {
	switch {
	case rng.Start == nil:
		return fmt.Sprintf("%s=-%d", RangeUnit, *rng.End)
	case rng.End == nil:
		return fmt.Sprintf("%s=%d-", RangeUnit, *rng.Start)
	default:
		return fmt.Sprintf("%s=%d-%d", RangeUnit, *rng.Start, *rng.End)
	}
}

// ContentRange represents a content range header.
type ContentRange struct {
	Start int64
	End   int64
	Size  int64
}

// ContentRangeFromRange returns a content range from a given range and content size.
// Start and end are normalized based on the given size and verified to be within bounds.
func ContentRangeFromRange(rng Range, size int64) (ContentRange, error) {
	if size <= 0 {
		return ContentRange{}, fmt.Errorf("size %d cannot be equal or less than zero", size)
	}
	if err := rng.Validate(); err != nil {
		return ContentRange{}, err
	}

	// Suffix length range.
	if rng.Start == nil {
		suffixLen := min(*rng.End, size)
		crng := ContentRange{
			Start: size - suffixLen,
			End:   size - 1,
			Size:  size,
		}
		return crng, nil
	}

	// Offset with unknown size.
	if rng.End == nil {
		crng := ContentRange{
			Start: *rng.Start,
			End:   size - 1,
			Size:  size,
		}
		return crng, nil
	}

	// Known start and end range.
	crng := ContentRange{
		Start: *rng.Start,
		End:   min(*rng.End, size-1),
		Size:  size,
	}
	return crng, nil
}

// String returns content range content as formatted header value.
func (crng ContentRange) String() string {
	return fmt.Sprintf("%s %d-%d/%d", RangeUnit, crng.Start, crng.End, crng.Size)
}

// Length returns the byte size of the content within the range.
// This should not be confused with size which is the total size of the content.
func (crng ContentRange) Length() int64 {
	return crng.End - crng.Start + 1
}
