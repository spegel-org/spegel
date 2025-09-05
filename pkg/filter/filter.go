package filter

import (
	"regexp"
	"strings"
)

// FilterString checks if a single string matches any of the provided regex patterns.
// It returns true if the string matches at least one pattern, false otherwise.
// If patterns is empty or nil, it returns true (no filtering).
// Invalid patterns are ignored (no error returned).
func FilterString(str string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}

	// Try to match against each pattern
	for _, pattern := range patterns {
		// Skip empty patterns
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		// Compile pattern and match - ignore compilation errors
		if compiled, err := regexp.Compile(pattern); err == nil {
			if compiled.MatchString(str) {
				return true
			}
		}
	}

	return false
}
