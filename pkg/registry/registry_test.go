package registry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	tlog "github.com/go-logr/logr/testing"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestRegistryOptions(t *testing.T) {
	t.Parallel()

	transport := &http.Transport{}
	log := logr.Discard()
	opts := []RegistryOption{
		WithResolveRetries(5),
		WithResolveLatestTag(true),
		WithResolveTimeout(10 * time.Minute),
		WithTransport(transport),
		WithLogger(log),
		WithBasicAuth("foo", "bar"),
	}
	cfg := RegistryConfig{}
	err := cfg.Apply(opts...)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.ResolveRetries)
	require.True(t, cfg.ResolveLatestTag)
	require.Equal(t, 10*time.Minute, cfg.ResolveTimeout)
	require.Equal(t, transport, cfg.Client.Transport)
	require.Equal(t, log, cfg.Log)
	require.Equal(t, "foo", cfg.Username)
	require.Equal(t, "bar", cfg.Password)
}

func TestReadyHandler(t *testing.T) {
	t.Parallel()

	router := routing.NewMemoryRouter(map[string][]netip.AddrPort{}, netip.MustParseAddrPort("127.0.0.1:8080"))
	reg, err := NewRegistry(nil, router)
	require.NoError(t, err)
	srv, err := reg.Server("")
	require.NoError(t, err)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	srv.Handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusInternalServerError, rw.Result().StatusCode)

	router.Add("foo", netip.MustParseAddrPort("127.0.0.1:9090"))
	rw = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	srv.Handler.ServeHTTP(rw, req)
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
			srv, err := reg.Server("")
			require.NoError(t, err)
			srv.Handler.ServeHTTP(rw, req)

			require.Equal(t, tt.expected, rw.Result().StatusCode)
		})
	}
}

func TestMirrorHandler(t *testing.T) {
	t.Parallel()

	badSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if r.Method == http.MethodGet {
			//nolint:errcheck // ignore
			w.Write([]byte("hello world"))
		}
	}))
	t.Cleanup(func() {
		badSvr.Close()
	})
	badAddrPort := netip.MustParseAddrPort(badSvr.Listener.Addr().String())
	goodSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(httpx.HeaderContentLength, "0")
		w.Header().Set(httpx.HeaderContentType, "application/octet-stream")
		w.Header().Set(oci.HeaderDockerDigest, ocispec.DescriptorEmptyJSON.Digest.String())
		if r.Method == http.MethodGet {
			b := []byte("hello world")
			w.Header().Set(httpx.HeaderContentLength, strconv.FormatInt(int64(len(b)), 10))
			//nolint:errcheck // ignore
			w.Write(b)
		}
	}))
	t.Cleanup(func() {
		goodSvr.Close()
	})
	goodAddrPort := netip.MustParseAddrPort(goodSvr.Listener.Addr().String())
	unreachableAddrPort := netip.MustParseAddrPort("127.0.0.1:0")

	resolver := map[string][]netip.AddrPort{
		// No working peers
		"sha256:c3e30fbcf3b231356a1efbd30a8ccec75134a7a8b45217ede97f4ff483540b04": {badAddrPort, unreachableAddrPort, badAddrPort},
		// First Peer
		"sha256:3b8a55c543ccc7ae01c47b1d35af5826a6439a9b91ab0ca96de9967759279896": {goodAddrPort, badAddrPort, badAddrPort},
		// First peer error
		"sha256:a0daab85ec30e2809a38c32fa676515aba22f481c56fda28637ae964ff398e3d": {unreachableAddrPort, goodAddrPort},
		// Last peer working
		"sha256:11242d2a347bf8ab30b9f92d5ca219bbbedf95df5a8b74631194561497c1fae8": {badAddrPort, badAddrPort, goodAddrPort},
	}
	router := routing.NewMemoryRouter(resolver, netip.AddrPort{})
	reg, err := NewRegistry(oci.NewMemory(), router, WithLogger(tlog.NewTestLogger(t)))
	require.NoError(t, err)

	tests := []struct {
		name           string
		key            string
		expectedBody   string
		expectedStatus int
	}{
		{
			name:           "request should timeout when no peers exists",
			key:            "no-peers",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "",
		},
		{
			name:           "request should not timeout and give 404 if all peers fail",
			key:            "sha256:c3e30fbcf3b231356a1efbd30a8ccec75134a7a8b45217ede97f4ff483540b04",
			expectedStatus: http.StatusNotFound,
			expectedBody:   "",
		},
		{
			name:           "request should work when first peer responds",
			key:            "sha256:3b8a55c543ccc7ae01c47b1d35af5826a6439a9b91ab0ca96de9967759279896",
			expectedStatus: http.StatusOK,
			expectedBody:   "hello world",
		},
		{
			name:           "second peer should respond when first gives error",
			key:            "sha256:a0daab85ec30e2809a38c32fa676515aba22f481c56fda28637ae964ff398e3d",
			expectedStatus: http.StatusOK,
			expectedBody:   "hello world",
		},
		{
			name:           "last peer should respond when two first fail",
			key:            "sha256:11242d2a347bf8ab30b9f92d5ca219bbbedf95df5a8b74631194561497c1fae8",
			expectedStatus: http.StatusOK,
			expectedBody:   "hello world",
		},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s-%s", method, tt.name), func(t *testing.T) {
				t.Parallel()

				target := fmt.Sprintf("http://example.com/v2/foo/bar/blobs/%s", tt.key)
				rw := httptest.NewRecorder()
				req := httptest.NewRequest(method, target, nil)
				srv, err := reg.Server("")
				require.NoError(t, err)
				srv.Handler.ServeHTTP(rw, req)

				resp := rw.Result()
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tt.expectedStatus, resp.StatusCode)

				if method == http.MethodGet {
					require.Equal(t, tt.expectedBody, string(b))
				}
				if method == http.MethodHead {
					require.Empty(t, b)
				}
			})
		}
	}
}
