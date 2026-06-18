package httpx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestLoadCerts(t *testing.T) {
	t.Parallel()

	ca, cert, err := LoadCerts("")
	require.NoError(t, err)
	require.Nil(t, ca)
	require.Nil(t, cert)

	validCA, err := os.ReadFile(filepath.Join("testdata", "certs", "ca.crt"))
	require.NoError(t, err)
	validCert, err := os.ReadFile(filepath.Join("testdata", "certs", "tls.crt"))
	require.NoError(t, err)
	validKey, err := os.ReadFile(filepath.Join("testdata", "certs", "tls.key"))
	require.NoError(t, err)
	badCA, err := os.ReadFile(filepath.Join("testdata", "certs", "bad-ca.crt"))
	require.NoError(t, err)

	//nolint: govet // Prioritize readability in tests.
	tests := []struct {
		name       string
		files      map[string][]byte
		expectErr  bool
		expectCA   bool
		expectCert bool
	}{
		{
			name: "ca only",
			files: map[string][]byte{
				CAFilename: validCA,
			},
			expectCA: true,
		},
		{
			name: "invalid ca",
			files: map[string][]byte{
				CAFilename: badCA,
			},
			expectErr: true,
		},
		{
			name: "cert without key",
			files: map[string][]byte{
				CertFilename: validCert,
			},
			expectErr: true,
		},
		{
			name: "key without cert",
			files: map[string][]byte{
				KeyFilename: validKey,
			},
			expectErr: true,
		},
		{
			name: "valid cert and key",
			files: map[string][]byte{
				CertFilename: validCert,
				KeyFilename:  validKey,
			},
			expectCert: true,
		},
		{
			name: "valid ca cert key",
			files: map[string][]byte{
				CAFilename:   validCA,
				CertFilename: validCert,
				KeyFilename:  validKey,
			},
			expectCA:   true,
			expectCert: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dirPath := t.TempDir()
			for name, data := range tt.files {
				err := os.WriteFile(filepath.Join(dirPath, name), data, 0o600)
				require.NoError(t, err)
			}

			ca, cert, err := LoadCerts(dirPath)

			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.expectCA {
				require.NotNil(t, ca)
			} else {
				require.Nil(t, ca)
			}

			if tt.expectCert {
				require.NotNil(t, cert)
				require.NotEmpty(t, cert.Certificate)
			} else {
				require.Nil(t, cert)
			}
		})
	}
}
