package registry

import (
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

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestRegistryOptions(t *testing.T) {
	t.Parallel()

	transport := &http.Transport{}
	filterStrings := []string{"^docker\\.io/", "^gcr\\.io/"}
	// Compile regex patterns
	var filters []*regexp.Regexp
	for _, pattern := range filterStrings {
		if compiled, err := regexp.Compile(pattern); err == nil {
			filters = append(filters, compiled)
		}
	}
	opts := []RegistryOption{
		WithResolveRetries(5),
		WithRegistryFilters(filters),
		WithResolveLatestTag(true),
		WithResolveTimeout(10 * time.Minute),
		WithTransport(transport),
		WithBasicAuth("foo", "bar"),
	}
	cfg := RegistryConfig{}
	err := cfg.Apply(opts...)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.ResolveRetries)
	require.Equal(t, filters, cfg.Filters)
	require.True(t, cfg.ResolveLatestTag)
	require.Equal(t, 10*time.Minute, cfg.ResolveTimeout)
	require.Equal(t, transport, cfg.Transport)
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
	require.Equal(t, http.StatusInternalServerError, rw.Result().StatusCode)
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/livez", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)

	router.Add("foo", netip.MustParseAddrPort("127.0.0.1:9090"))
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/readyz", nil)
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
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

	badReg, err := NewRegistry(oci.NewMemory(), routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{}))
	require.NoError(t, err)
	badSvr := httptest.NewServer(badReg.Handler(logr.Discard()))
	t.Cleanup(func() {
		badSvr.Close()
	})
	badAddrPort := netip.MustParseAddrPort(badSvr.Listener.Addr().String())

	memStore := oci.NewMemory()
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6"), MediaType: "dummy"}, []byte("no working peers"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67"), MediaType: "dummy"}, []byte("first peer"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2"), MediaType: "dummy"}, []byte("second peer"))
	require.NoError(t, err)
	err = memStore.Write(ocispec.Descriptor{Digest: digest.Digest("sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba"), MediaType: "dummy"}, []byte("last peer working"))
	require.NoError(t, err)
	goodReg, err := NewRegistry(memStore, routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.AddrPort{}))
	require.NoError(t, err)
	goodSvr := httptest.NewServer(goodReg.Handler(logr.Discard()))
	t.Cleanup(func() {
		goodSvr.Close()
	})
	goodAddrPort := netip.MustParseAddrPort(goodSvr.Listener.Addr().String())

	unreachableAddrPort := netip.MustParseAddrPort("127.0.0.1:0")

	resolver := map[string][]netip.AddrPort{
		// No working peers
		"sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6": {badAddrPort, unreachableAddrPort, badAddrPort},
		// First peer
		"sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67": {goodAddrPort, badAddrPort, badAddrPort},
		// Second peer
		"sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2": {unreachableAddrPort, goodAddrPort},
		// Last peer working
		"sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba": {badAddrPort, badAddrPort, goodAddrPort},
	}
	router := routing.NewMemoryRouter(resolver, netip.AddrPort{})
	reg, err := NewRegistry(oci.NewMemory(), router)
	require.NoError(t, err)
	handler := reg.Handler(logr.Discard())

	tests := []struct {
		expectedHeaders http.Header
		name            string
		key             string
		expectedBody    []byte
		expectedStatus  int
	}{
		{
			name:            "request should timeout when no peers exists",
			key:             "sha256:03ffdf45276dd38ffac79b0e9c6c14d89d9113ad783d5922580f4c66a3305591",
			expectedStatus:  http.StatusNotFound,
			expectedBody:    []byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","detail":{"attempts":0},"message":"mirror with image component sha256:03ffdf45276dd38ffac79b0e9c6c14d89d9113ad783d5922580f4c66a3305591 could not be found"}]}`),
			expectedHeaders: nil,
		},
		{
			name:            "request should not timeout and give 404 if all peers fail",
			key:             "sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6",
			expectedStatus:  http.StatusNotFound,
			expectedBody:    []byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","detail":{"attempts":3},"message":"mirror with image component sha256:18ca1296b9cc90d29b51b4a8724d97aa055102c3d74e53a8eafb3904c079c0c6 could not be found requests to 3 mirrors failed, all attempts have been exhausted or timeout has been reached"}]}`),
			expectedHeaders: nil,
		},
		{
			name:           "request should work when first peer responds",
			key:            "sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67",
			expectedStatus: http.StatusOK,
			expectedBody:   []byte("first peer"),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeBinary},
				httpx.HeaderContentLength: {"10"},
				oci.HeaderDockerDigest:    {"sha256:0b7e0ac6364af64af017531f137a95f3a5b12ea38be0e74a860004d3e5760a67"},
			},
		},
		{
			name:           "second peer should respond when first gives error",
			key:            "sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2",
			expectedStatus: http.StatusOK,
			expectedBody:   []byte("second peer"),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeBinary},
				httpx.HeaderContentLength: {"11"},
				oci.HeaderDockerDigest:    {"sha256:431491e49ba5fa61930417a46b24c03b6df0b426b90009405457741ac52f44b2"},
			},
		},
		{
			name:           "last peer should respond when two first fail",
			key:            "sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba",
			expectedStatus: http.StatusOK,
			expectedBody:   []byte("last peer working"),
			expectedHeaders: http.Header{
				httpx.HeaderContentType:   {httpx.ContentTypeBinary},
				httpx.HeaderContentLength: {"17"},
				oci.HeaderDockerDigest:    {"sha256:7d66cda2ba857d07e5530e53565b7d56b10ab80d16b6883fff8478327a49b4ba"},
			},
		},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s-%s", method, tt.name), func(t *testing.T) {
				t.Parallel()

				target := fmt.Sprintf("http://example.com/v2/foo/bar/blobs/%s", tt.key)
				rw := httptest.NewRecorder()
				req := httptest.NewRequest(method, target, nil)
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
				for k, v := range tt.expectedHeaders {
					require.Equal(t, v, resp.Header.Values(k))
				}
			})
		}
	}

}
