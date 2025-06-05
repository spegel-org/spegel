package httpx

import (
	"fmt"
	"strings"
)

type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func FormatRangeHeader(byteRange ByteRange) string {
	return fmt.Sprintf("bytes=%d-%d", byteRange.Start, byteRange.End)
}

func FormatMultipartRangeHeader(byteRanges []ByteRange) string {
	if len(byteRanges) == 0 {
		return ""
	}
	ranges := []string{}
	for _, br := range byteRanges {
		ranges = append(ranges, fmt.Sprintf("%d-%d", br.Start, br.End))
	}
	return "bytes=" + strings.Join(ranges, ", ")
}
