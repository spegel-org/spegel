package oci

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
)

var (
	wildcardRegistries  = []string{"_default", "*"}
	wildcardRegistryURL = url.URL{Host: wildcardRegistries[0]}
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

type RegistryWhitelistFilter struct {
	Whitelist []string
}

func (f RegistryWhitelistFilter) Matches(ref Reference) bool {
	return !slices.Contains(f.Whitelist, ref.Registry)
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

// FilterForMirroredRegistries returns a filter that matches only to the registries that have been configured to be mirrored.
// If the slice is empty or contains a wildcard registry nil will be returned as no registry should be filtered.
func FilterForMirroredRegistries(mirroredRegistries []string) (*RegistryWhitelistFilter, error) {
	if len(mirroredRegistries) == 0 {
		return nil, nil
	}
	rus, err := parseRegistries(mirroredRegistries, true)
	if err != nil {
		return nil, err
	}
	registryHosts := []string{}
	for _, ru := range rus {
		// No registry filter when wildcard is part of mirrored registries.
		if ru == wildcardRegistryURL {
			return nil, nil
		}
		registryHosts = append(registryHosts, ru.Host)
	}
	return &RegistryWhitelistFilter{Whitelist: registryHosts}, nil
}

func parseRegistries(registries []string, allowWildcard bool) ([]url.URL, error) {
	if len(registries) == 0 && allowWildcard {
		return []url.URL{wildcardRegistryURL}, nil
	}
	rus := []url.URL{}
	hasWildcard := false
	for _, s := range registries {
		if slices.Contains(wildcardRegistries, s) {
			if !allowWildcard {
				return nil, errors.New("wildcard registries are not allowed")
			}
			if hasWildcard {
				return nil, errors.New("registries should not contain two wildcards")
			}
			hasWildcard = true
			rus = append(rus, wildcardRegistryURL)
			continue
		}
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		err = validateRegistryURL(u)
		if err != nil {
			return nil, err
		}
		rus = append(rus, *u)
	}
	return rus, nil
}

func validateRegistryURL(u *url.URL) error {
	errs := []error{}
	if u.Scheme != "http" && u.Scheme != "https" {
		errs = append(errs, fmt.Errorf("invalid registry url scheme must be http or https: %s", u.String()))
	}
	if u.Path != "" {
		errs = append(errs, fmt.Errorf("invalid registry url path has to be empty: %s", u.String()))
	}
	if len(u.Query()) != 0 {
		errs = append(errs, fmt.Errorf("invalid registry url query has to be empty: %s", u.String()))
	}
	if u.User != nil {
		errs = append(errs, fmt.Errorf("invalid registry url user has to be empty: %s", u.String()))
	}
	return errors.Join(errs...)
}
