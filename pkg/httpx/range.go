package httpx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const RangeUnit = "bytes"

type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (rng Range) String() string {
	return fmt.Sprintf("%s=%d-%d", RangeUnit, rng.Start, rng.End)
}

func (rng Range) Size() int64 {
	return rng.End - rng.Start + 1
}

func ParseRangeHeader(h string, size int64) (Range, error) {
	if size <= 0 {
		return Range{}, fmt.Errorf("size %d cannot be equal or less than zero", size)
	}
	rangeUnitPrefix := RangeUnit + "="
	if !strings.HasPrefix(h, rangeUnitPrefix) {
		return Range{}, errors.New("invalid range unit")
	}
	ranges := strings.Split(strings.TrimPrefix(h, rangeUnitPrefix), ",")
	if len(ranges) != 1 {
		return Range{}, errors.New("multiple ranges not supported")
	}
	parts := strings.SplitN(strings.TrimSpace(ranges[0]), "-", 2)
	if len(parts) != 2 {
		return Range{}, errors.New("invalid range format")
	}

	// Suffix byte range
	if parts[0] == "" {
		suffixLen, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Range{}, err
		}
		if suffixLen <= 0 {
			return Range{}, fmt.Errorf("invalid suffix range %d", suffixLen)
		}
		if suffixLen > size {
			suffixLen = size
		}
		rng := Range{
			Start: size - suffixLen,
			End:   size - 1,
		}
		return rng, nil
	}

	rng := Range{}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Range{}, err
	}
	if start < 0 {
		return Range{}, fmt.Errorf("invalid start %d", start)
	}
	rng.Start = start
	if parts[1] == "" {
		rng.End = size - 1
	} else {
		end, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Range{}, err
		}
		if end >= size {
			end = size - 1
		}
		rng.End = end
	}
	if rng.Start > rng.End {
		return Range{}, fmt.Errorf("start %d cannot be larger than end %d", rng.Start, rng.End)
	}
	return rng, nil
}

type ContentRange struct {
	Start int64
	End   int64
	Size  int64
}

func ContentRangeFromRange(rng Range, size int64) ContentRange {
	return ContentRange{
		Start: rng.Start,
		End:   rng.End,
		Size:  size,
	}
}

func (crng ContentRange) String() string {
	return fmt.Sprintf("%s %d-%d/%d", RangeUnit, crng.Start, crng.End, crng.Size)
}
