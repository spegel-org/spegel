package oci

import (
	"regexp"
)

// MatchesFilter returns true if the reference matches any of the regexes.
// The reference if converted to a string in the format of registry/repository[:tag].
func MatchesFilter(ref Reference, filters []*regexp.Regexp) bool {
	str := ref.Registry + "/" + ref.Repository
	if ref.Tag != "" {
		str += ":" + ref.Tag
	}
	for _, f := range filters {
		if f.MatchString(str) {
			return true
		}
	}
	return false
}
