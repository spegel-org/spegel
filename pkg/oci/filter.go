package oci

import (
	"regexp"
)

type Filter interface {
	Matches(ref Reference) bool
}

var _ Filter = RegexFilter{}

type RegexFilter struct {
	Regex *regexp.Regexp
}

func (f RegexFilter) Matches(ref Reference) bool {
	// The reference if converted to a string in the format of registry/repository[:tag].
	str := ref.Registry + "/" + ref.Repository
	if ref.Tag != "" {
		str += ":" + ref.Tag
	}
	if f.Regex.MatchString(str) {
		return true
	}
	return false
}

// MatchesFilter returns true if the reference matches any of the regexes.
func MatchesFilter(ref Reference, filters []Filter) bool {
	for _, f := range filters {
		if f.Matches(ref) {
			return true
		}
	}
	return false
}
