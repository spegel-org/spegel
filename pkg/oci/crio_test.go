package oci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCRIoClientLoadCacheMetadata(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns empty metadata", func(t *testing.T) {
		t.Parallel()
		client := &CRIoClient{imageContentCacheDir: t.TempDir()}
		metadata, err := client.loadCacheMetadata()
		require.NoError(t, err)
		require.NotNil(t, metadata.Blobs)
		require.Empty(t, metadata.Blobs)
	})

	t.Run("parses valid metadata", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		metadata := CRIOCacheMetadata{
			Blobs: map[string]CRIOBlobMetadata{
				"sha256:abc123": {
					Digest: "sha256:abc123",
					Size:   1024,
					Sources: []CRIOBlobSource{
						{Registry: "docker.io", Repository: "library/alpine"},
					},
				},
			},
		}
		data, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "metadata.json"), data, 0644))

		client := &CRIoClient{imageContentCacheDir: tmpDir}
		result, err := client.loadCacheMetadata()
		require.NoError(t, err)
		require.Len(t, result.Blobs, 1)
		require.Equal(t, "docker.io", result.Blobs["sha256:abc123"].Sources[0].Registry)
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "metadata.json"), []byte("{invalid"), 0644))

		client := &CRIoClient{imageContentCacheDir: tmpDir}
		_, err := client.loadCacheMetadata()
		require.Error(t, err)
	})
}

func TestExtractRegistryFromRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fullRepo         string
		expectedRegistry string
		expectedRepo     string
	}{
		{"docker.io/library/alpine", "docker.io", "library/alpine"},
		{"ghcr.io/spegel-org/spegel", "ghcr.io", "spegel-org/spegel"},
		{"localhost:5000/myimage", "localhost:5000", "myimage"},
		{"localhost/myimage", "localhost", "myimage"},
		{"library/alpine", "", "library/alpine"},
		{"alpine", "", "alpine"},
		{"my-registry.example.com/org/image", "my-registry.example.com", "org/image"},
	}

	for _, tt := range tests {
		t.Run(tt.fullRepo, func(t *testing.T) {
			t.Parallel()
			registry, repo := extractRegistryFromRepo(tt.fullRepo)
			require.Equal(t, tt.expectedRegistry, registry)
			require.Equal(t, tt.expectedRepo, repo)
		})
	}
}

func TestCRIoClientVerify(t *testing.T) {
	t.Parallel()

	t.Run("succeeds with valid blobs directory", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "blobs"), 0755))

		client := &CRIoClient{imageContentCacheDir: tmpDir}
		require.NoError(t, client.Verify(t.Context(), ""))
	})

	t.Run("fails when blobs directory missing", func(t *testing.T) {
		t.Parallel()
		client := &CRIoClient{imageContentCacheDir: t.TempDir()}
		err := client.Verify(t.Context(), "")
		require.ErrorContains(t, err, "does not exist")
	})
}
