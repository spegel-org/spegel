package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
)

const (
	CAFilename   = "ca.crt"
	CertFilename = "tls.crt"
	KeyFilename  = "tls.key"
)

// LoadCerts returns the CA and TLS certificates found in the given base directory.
func LoadCerts(dirPath string) (*x509.CertPool, *tls.Certificate, error) {
	if dirPath == "" {
		return nil, nil, nil
	}

	caData, err := os.ReadFile(filepath.Join(dirPath, CAFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	var pool *x509.CertPool
	if len(caData) > 0 {
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, nil, errors.New("no valid CA certs found")
		}
	}

	certData, err := os.ReadFile(filepath.Join(dirPath, CertFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	keyData, err := os.ReadFile(filepath.Join(dirPath, KeyFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if len(certData) == 0 && len(keyData) == 0 {
		return pool, nil, nil
	}
	if len(certData) == 0 || len(keyData) == 0 {
		return nil, nil, errors.New("both client TLS certificate and client TLS key must be provided")
	}
	cert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, nil, err
	}

	return pool, &cert, nil
}
