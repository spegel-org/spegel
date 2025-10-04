package oci

import (
	"errors"
	"fmt"

	"github.com/opencontainers/go-digest"
)

// Reference represents the lowest level of reference to OCI content.
type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

// Validate checks that the contents of the reference is valid.
func (r Reference) Validate() error {
	if r.Registry == "" {
		return errors.New("reference needs to contain a registry")
	}
	if r.Repository == "" {
		return errors.New("reference needs to contain a repository")
	}
	if r.Digest != "" {
		if err := r.Digest.Validate(); err != nil {
			return err
		}
	}
	if r.Digest == "" && r.Tag == "" {
		return errors.New("either tag or digest has to be set")
	}
	return nil
}

// Identifier returns the digest if set or alternatively if not the full image reference with the tag.
func (r Reference) Identifier() string {
	if r.Digest != "" {
		return r.Digest.String()
	}
	return fmt.Sprintf("%s/%s:%s", r.Registry, r.Repository, r.Tag)
}
