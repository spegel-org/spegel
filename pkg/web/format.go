package web

import (
	"fmt"
	"strings"
	"time"
)

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}

	values := []int64{
		int64(d / (24 * time.Hour)),
		int64((d % (24 * time.Hour)) / time.Hour),
		int64((d % time.Hour) / time.Minute),
		int64((d % time.Minute) / time.Second),
		int64((d % time.Second) / time.Millisecond),
	}
	units := []string{
		"d",
		"h",
		"m",
		"s",
		"ms",
	}

	comps := []string{}
	for i, v := range values {
		if v == 0 {
			if len(comps) > 0 {
				break
			}
			continue
		}
		comps = append(comps, fmt.Sprintf("%d%s", v, units[i]))
		if len(comps) == 2 {
			break
		}
	}
	return strings.Join(comps, " ")
}
