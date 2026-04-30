package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
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

// MirrorTarget represents a single mirror endpoint that Spegel should write
// into containerd's hosts.toml. It can be expressed either as a plain URL
// string or as a struct with additional options such as OverridePath, which
// is required when the mirror URL contains a path prefix (for example AWS
// ECR pull-through cache, where the upstream URL is shaped as
// <account>.dkr.ecr.<region>.amazonaws.com/v2/<cache-prefix>).
type MirrorTarget struct {
	URL          string `json:"url" yaml:"url"`
	OverridePath bool   `json:"overridePath" yaml:"overridePath"`
}

// UnmarshalJSON allows MirrorTarget to be decoded either from a plain JSON
// string (backwards compatible) or from a JSON object with a url field.
func (m *MirrorTarget) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		m.URL = s
		m.OverridePath = false
		return nil
	}
	type raw MirrorTarget
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*m = MirrorTarget(r)
	return nil
}

// parseMirrorTarget parses a single mirror target entry. The entry can be a
// plain URL or a JSON object of the form {"url":"...","overridePath":true}.
// This allows Helm charts to configure the new struct form without changing
// the underlying CLI flag type.
func parseMirrorTarget(s string) (MirrorTarget, error) {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") {
		var mt MirrorTarget
		if err := json.Unmarshal([]byte(trimmed), &mt); err != nil {
			return MirrorTarget{}, fmt.Errorf("invalid mirror target JSON: %w", err)
		}
		if mt.URL == "" {
			return MirrorTarget{}, errors.New("invalid mirror target: url is required")
		}
		return mt, nil
	}
	return MirrorTarget{URL: trimmed}, nil
}

// parseMirrorTargets parses the raw mirror target entries supplied through
// the --mirror-targets flag, validating each URL. Mirror targets are not
// allowed to contain a path unless OverridePath is set, in which case the
// path is forwarded as-is to containerd through hosts.toml.
func parseMirrorTargets(entries []string) ([]parsedMirrorTarget, error) {
	out := []parsedMirrorTarget{}
	for _, e := range entries {
		mt, err := parseMirrorTarget(e)
		if err != nil {
			return nil, err
		}
		u, err := url.Parse(mt.URL)
		if err != nil {
			return nil, err
		}
		if err := validateMirrorTargetURL(u, mt.OverridePath); err != nil {
			return nil, err
		}
		out = append(out, parsedMirrorTarget{URL: *u, OverridePath: mt.OverridePath})
	}
	return out, nil
}

// parsedMirrorTarget is the internal representation handed to templateHosts.
type parsedMirrorTarget struct {
	URL          url.URL
	OverridePath bool
}

func validateMirrorTargetURL(u *url.URL, overridePath bool) error {
	errs := []error{}
	if u.Scheme != "http" && u.Scheme != "https" {
		errs = append(errs, fmt.Errorf("invalid registry url scheme must be http or https: %s", u.String()))
	}
	if u.Path != "" && !overridePath {
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
