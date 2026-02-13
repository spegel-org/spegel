package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/option"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestRegistryOptions(t *testing.T) {
	t.Parallel()

	filters := []oci.Filter{
		oci.RegexFilter{Regex: regexp.MustCompile(`^docker.io/`)},
		oci.RegexFilter{Regex: regexp.MustCompile(`^gcr.io/`)},
	}
	ociClient, err := oci.NewClient()
	require.NoError(t, err)

	opts := []RegistryOption{
		WithResolveRetries(5),
		WithRegistryFilters(filters),
		WithResolveTimeout(10 * time.Minute),
		WithBasicAuth("foo", "bar"),
		WithOCIClient(ociClient),
	}
	cfg := RegistryConfig{}
	err = option.Apply(&cfg, opts...)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.ResolveRetries)
	require.Equal(t, filters, cfg.Filters)
	require.Equal(t, 10*time.Minute, cfg.ResolveTimeout)
	require.Equal(t, ociClient, cfg.OCIClient)
	require.Equal(t, "foo", cfg.Username)
	require.Equal(t, "bar", cfg.Password)
}

func TestProbeHandlers(t *testing.T) {
	t.Parallel()

	router := routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.MustParseAddrPort("127.0.0.1:8080"))
	reg, err := NewRegistry(nil, router)
	require.NoError(t, err)
	handler := reg.Handler(logr.Discard())

	rw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/readyz", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/livez", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)

	router.SetReadiness(false)
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/readyz", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusInternalServerError, rw.Result().StatusCode)
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/livez", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		username    string
		password    string
		reqUsername string
		reqPassword string
		expected    int
	}{
		{
			name:     "no registry authentication",
			expected: http.StatusOK,
		},
		{
			name:        "unnecessary authentication",
			reqUsername: "foo",
			reqPassword: "bar",
			expected:    http.StatusOK,
		},
		{
			name:        "correct authentication",
			username:    "foo",
			password:    "bar",
			reqUsername: "foo",
			reqPassword: "bar",
			expected:    http.StatusOK,
		},
		{
			name:        "invalid username",
			username:    "foo",
			password:    "bar",
			reqUsername: "wrong",
			reqPassword: "bar",
			expected:    http.StatusUnauthorized,
		},
		{
			name:        "invalid password",
			username:    "foo",
			password:    "bar",
			reqUsername: "foo",
			reqPassword: "wrong",
			expected:    http.StatusUnauthorized,
		},
		{
			name:     "missing authentication",
			username: "foo",
			password: "bar",
			expected: http.StatusUnauthorized,
		},
		{
			name:     "missing authentication",
			username: "foo",
			password: "bar",
			expected: http.StatusUnauthorized,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg, err := NewRegistry(nil, nil, WithBasicAuth(tt.username, tt.password))
			require.NoError(t, err)
			rw := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://localhost/v2/", nil)
			req.SetBasicAuth(tt.reqUsername, tt.reqPassword)
			handler := reg.Handler(logr.Discard())
			handler.ServeHTTP(rw, req)

			require.Equal(t, tt.expected, rw.Result().StatusCode)
		})
	}
}

func TestRegistryHandler(t *testing.T) {
	t.Parallel()

	unreachableAddrPort := netip.MustParseAddrPort("127.0.0.1:0")

	badAddrPorts := []netip.AddrPort{}
	for range 2 {
		badReg, err := NewRegistry(oci.NewMemory(), routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{}))
		require.NoError(t, err)
		badSvr := httptest.NewServer(badReg.Handler(logr.Discard()))
		t.Cleanup(func() {
			badSvr.Close()
		})
		badAddrPort := netip.MustParseAddrPort(badSvr.Listener.Addr().String())
		badAddrPorts = append(badAddrPorts, badAddrPort)
	}

	memStore := oci.NewMemory()
	err := memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6"), MediaType: "dummy"}, []byte("no working peers"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67"), MediaType: "dummy"}, []byte("first peer"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2"), MediaType: "dummy"}, []byte("second peer"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba"), MediaType: "dummy"}, []byte("last peer working"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2"), MediaType: "application/vnd.oci.image.index.v1+json"}, []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83"), MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}, []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	require.NoError(t, err)
	goodReg, err := NewRegistry(memStore, routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{}))
	require.NoError(t, err)
	goodSvr := httptest.NewServer(goodReg.Handler(logr.Discard()))
	t.Cleanup(func() {
		goodSvr.Close()
	})
	goodAddrPort := netip.MustParseAddrPort(goodSvr.Listener.Addr().String())

	flakyAddrPorts := []netip.AddrPort{}
	for range 3 {
		flakyStore := &flakyStore{Memory: oci.NewMemory()}
		err = flakyStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:68a2f9c5f175c838c5e9433dfe7b9d3a73caade76b2185a8d9164405c5286edd"), MediaType: "dummy"}, []byte("Only a single peer"))
		require.NoError(t, err)
		err = flakyStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0"), MediaType: "dummy"}, []byte("Lorem Ipsum Dolor"))
		require.NoError(t, err)
		flakyReg, err := NewRegistry(flakyStore, routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{}))
		require.NoError(t, err)
		flakySvr := httptest.NewServer(flakyReg.Handler(logr.Discard()))
		t.Cleanup(func() {
			flakySvr.Close()
		})
		flakyAddrPort := netip.MustParseAddrPort(flakySvr.Listener.Addr().String())
		flakyAddrPorts = append(flakyAddrPorts, flakyAddrPort)
	}

	resolver := map[string][]netip.AddrPort{
		// No working peers.
		"sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6": {badAddrPorts[0], unreachableAddrPort, badAddrPorts[1]},
		// First peer.
		"sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67": {goodAddrPort, badAddrPorts[0], badAddrPorts[1]},
		// Second peer.
		"sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2": {unreachableAddrPort, goodAddrPort},
		// Last peer working.
		"sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba": {badAddrPorts[0], badAddrPorts[1], goodAddrPort},
		// Valid manifest and blob.
		"sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2": {goodAddrPort},
		"sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83": {goodAddrPort},
		// Flaky content.
		"sha256:68a2f9c5f175c838c5e9433dfe7b9d3a73caade76b2185a8d9164405c5286edd": {flakyAddrPorts[0]},
		"sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0": flakyAddrPorts,
	}
	router := routing.NewMemoryRouter(resolver, netip.AddrPort{})
	reg, err := NewRegistry(oci.NewMemory(), router, WithRegistryFilters([]oci.Filter{oci.RegexFilter{Regex: regexp.MustCompile(`:latest$`)}}))
	require.NoError(t, err)
	handler := reg.Handler(logr.Discard())

	//nolint: govet // Prioritize readability in tests.
	tests := []struct {
		name             string
		key              string
		distributionKind oci.DistributionKind
		rng              *httpx.Range
		expectedStatus   int
		expectedHeaders  http.Header
		expectedBody     []byte
	}{
		{
			name:             "request should timeout when no peers exists",
			key:              "sha256:03ffdf45276dd38ffac79b0e9c6c14d89d9113ad783d5922580f4c66a3305591",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte(`{"errors":[{"code":"BLOB_UNKNOWN","detail":{"attempts":0},"message":"could not find peer for sha256:03ffdf45276dd38ffac79b0e9c6c14d89d9113ad783d5922580f4c66a3305591"}]}`),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeJSON},
				httpx.HeaderContentLength: {"168"},
			},
		},
		{
			name:             "request should not timeout and give 404 if all peers fail",
			key:              "sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte(`{"errors":[{"code":"BLOB_UNKNOWN","detail":{"attempts":3},"message":"all request retries exhausted for sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6"}]}`),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeJSON},
				httpx.HeaderContentLength: {"178"},
			},
		},
		{
			name:             "request should work when first peer responds",
			key:              "sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte("first peer"),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {"dummy"},
				httpx.HeaderContentLength: {"10"},
				oci.HeaderDockerDigest:    {"sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67"},
			},
		},
		{
			name:             "second peer should respond when first gives error",
			key:              "sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte("second peer"),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {"dummy"},
				httpx.HeaderContentLength: {"11"},
				oci.HeaderDockerDigest:    {"sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2"},
			},
		},
		{
			name:             "last peer should respond when two first fail",
			key:              "sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte("last peer working"),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {"dummy"},
				httpx.HeaderContentLength: {"17"},
				oci.HeaderDockerDigest:    {"sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba"},
			},
		},
		{
			name:             "latest tag is supposed to be filtered",
			key:              "latest",
			distributionKind: oci.DistributionKindManifest,
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte{},
			expectedHeaders: http.Header{
				httpx.HeaderContentLength: {"0"},
			},
		},
		{
			name:             "path is invalid and cant be parsed",
			key:              "sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba",
			distributionKind: "invalid",
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte{},
			expectedHeaders: http.Header{
				httpx.HeaderContentLength: {"0"},
			},
		},
		{
			name:             "manifest requested as blob should not be found",
			key:              "sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte(`{"errors":[{"code":"BLOB_UNKNOWN","detail":{"attempts":1},"message":"all request retries exhausted for sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2"}]}`),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeJSON},
				httpx.HeaderContentLength: {"178"},
			},
		},
		{
			name:             "existing manifest should be found",
			key:              "sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2",
			distributionKind: oci.DistributionKindManifest,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {"application/vnd.oci.image.index.v1+json"},
				httpx.HeaderContentLength: {"88"},
				oci.HeaderDockerDigest:    {"sha256:dff9de10919148711140d349bf03f1a99eb06f94b03e51715ccebfa7cdc518e2"},
			},
		},
		{
			name:             "blob requested as manifest should not be found",
			key:              "sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83",
			distributionKind: oci.DistributionKindManifest,
			expectedStatus:   http.StatusNotFound,
			expectedBody:     []byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","detail":{"attempts":1},"message":"all request retries exhausted for sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83"}]}`),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeJSON},
				httpx.HeaderContentLength: {"182"},
			},
		},
		{
			name:             "blob request with range",
			key:              "sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83",
			distributionKind: oci.DistributionKindBlob,
			rng:              &httpx.Range{Start: 1, End: 3},
			expectedStatus:   http.StatusPartialContent,
			expectedBody:     []byte{0x8b, 0x8, 0x0},
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {httpx.ContentTypeBinary},
				httpx.HeaderContentLength: {"3"},
				httpx.HeaderContentRange:  {"bytes 1-3/20"},
				oci.HeaderDockerDigest:    {"sha256:ac73670af3abed54ac6fb4695131f4099be9fbe39d6076c5d0264a6bbdae9d83"},
			},
		},
		{
			name:             "flaky reader with one peers should fail",
			key:              "sha256:68a2f9c5f175c838c5e9433dfe7b9d3a73caade76b2185a8d9164405c5286edd",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte("Only a "),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {"dummy"},
				httpx.HeaderContentLength: {"18"},
				oci.HeaderDockerDigest:    {"sha256:68a2f9c5f175c838c5e9433dfe7b9d3a73caade76b2185a8d9164405c5286edd"},
			},
		},
		{
			name:             "flaky reader with multiple peers should resume with next peer",
			key:              "sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0",
			distributionKind: oci.DistributionKindBlob,
			expectedStatus:   http.StatusOK,
			expectedBody:     []byte("Lorem Ipsum Dolor"),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {"dummy"},
				httpx.HeaderContentLength: {"17"},
				oci.HeaderDockerDigest:    {"sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0"},
			},
		},
		{
			name:             "flaky reader with range should return partial",
			key:              "sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0",
			distributionKind: oci.DistributionKindBlob,
			rng:              &httpx.Range{Start: 2, End: 15},
			expectedStatus:   http.StatusPartialContent,
			expectedBody:     []byte("rem Ipsum Dolo"),
			expectedHeaders: http.Header{
				httpx.HeaderAcceptRanges:  {httpx.RangeUnit},
				httpx.HeaderContentType:   {httpx.ContentTypeBinary},
				httpx.HeaderContentLength: {"14"},
				httpx.HeaderContentRange:  {"bytes 2-15/17"},
				oci.HeaderDockerDigest:    {"sha256:c8dc81dabe7ad5e801191aade7c87fb806d0ef9ce9b699d2e9598337f57f14d0"},
			},
		},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s-%s", method, tt.name), func(t *testing.T) {
				t.Parallel()

				target := fmt.Sprintf("http://example.com/v2/foo/bar/%s/%s?ns=docker.io", tt.distributionKind, tt.key)
				rw := httptest.NewRecorder()
				req := httptest.NewRequest(method, target, nil)
				if tt.rng != nil {
					req.Header.Set(httpx.HeaderRange, tt.rng.String())
				}
				handler.ServeHTTP(rw, req)

				resp := rw.Result()
				defer httpx.DrainAndClose(resp.Body)
				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tt.expectedStatus, resp.StatusCode)

				switch method {
				case http.MethodGet:
					require.Equal(t, tt.expectedBody, b)
				case http.MethodHead:
					require.Empty(t, b)
				default:
					t.FailNow()
				}

				if tt.expectedHeaders == nil {
					require.Empty(t, resp.Header)
				}
				require.Equal(t, tt.expectedHeaders, resp.Header)
			})
		}
	}
}

type flakyStore struct {
	*oci.Memory
}

func (s *flakyStore) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	rc, err := s.Memory.Open(ctx, dgst)
	if err != nil {
		return nil, err
	}
	desc, err := s.Descriptor(ctx, dgst)
	if err != nil {
		return nil, err
	}
	rc = &flakyReadSeekCloser{ReadSeekCloser: rc, limit: int(0.4 * float64(desc.Size))}
	return rc, nil
}

type flakyReadSeekCloser struct {
	io.ReadSeekCloser
	limit     int
	readSoFar int
}

func (f *flakyReadSeekCloser) Read(p []byte) (int, error) {
	if f.readSoFar >= f.limit {
		return 0, io.ErrUnexpectedEOF
	}

	// Limit how much can be read this call
	remaining := f.limit - f.readSoFar
	if len(p) > remaining {
		p = p[:remaining]
	}

	n, err := f.ReadSeekCloser.Read(p)
	f.readSoFar += n

	// If we hit the limit, force an unexpected EOF
	if f.readSoFar >= f.limit {
		return n, io.ErrUnexpectedEOF
	}

	return n, err
}
