package storetest

import (
	"io"
	"testing"

	"github.com/go-openapi/testify/v2/require"
	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/store"
)

type ProviderConfig struct {
	Name               string
	NotFoundRef        string
	ExistingRef        string
	ExistingRefDigest  digest.Digest
	NotFoundDigest     digest.Digest
	ExistingDescriptor store.Descriptor
}

func ProviderConformance(t *testing.T, provider store.Provider, cfg ProviderConfig) {
	t.Helper()

	require.NotEmpty(t, cfg.Name)
	require.NotEmpty(t, cfg.NotFoundRef)
	require.NotEmpty(t, cfg.NotFoundDigest)
	require.NotEmpty(t, cfg.ExistingDescriptor)

	name := provider.Name()
	require.EqualT(t, cfg.Name, name)

	dgst, err := provider.Resolve(t.Context(), cfg.NotFoundRef)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Empty(t, dgst)
	if cfg.ExistingRef != "" {
		dgst, err = provider.Resolve(t.Context(), cfg.ExistingRef)
		require.NoError(t, err)
		require.EqualT(t, cfg.ExistingRefDigest, dgst)
	}

	desc, err := provider.Descriptor(t.Context(), cfg.NotFoundDigest)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Empty(t, desc)
	rc, err := provider.Open(t.Context(), cfg.NotFoundDigest)
	require.Nil(t, rc)
	require.ErrorIs(t, err, store.ErrNotFound)

	desc, err = provider.Descriptor(t.Context(), cfg.ExistingDescriptor.Digest)
	require.NoError(t, err)
	require.EqualT(t, cfg.ExistingDescriptor, desc)
	rc, err = provider.Open(t.Context(), cfg.ExistingDescriptor.Digest)
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	rc.Close()
	require.EqualT(t, cfg.ExistingDescriptor.Digest, digest.FromBytes(b))
}
