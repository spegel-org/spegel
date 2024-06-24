package registry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/mux"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	ociClient := oci.NewMockClient(nil)
	router := routing.NewMemoryRouter(nil, netip.AddrPort{})

	reg := NewRegistry(ociClient, router)
	require.Equal(t, logr.Discard(), reg.log)
	require.Nil(t, reg.throttler)
	require.Equal(t, ociClient, reg.ociClient)
	require.Equal(t, router, reg.router)
	require.Nil(t, reg.transport)
	require.Empty(t, reg.localAddr)
	require.Equal(t, 3, reg.resolveRetries)
	require.Equal(t, 20*time.Millisecond, reg.resolveTimeout)
	require.True(t, reg.resolveLatestTag)

	srv, err := reg.Server(":443")
	require.NoError(t, err)
	require.Equal(t, ":443", srv.Addr)

	log := funcr.New(func(prefix, args string) {}, funcr.Options{})
	trans := &http.Transport{}
	opts := []Option{
		WithLogger(log),
		WithTransport(trans),
		WithLocalAddress("foobar"),
		WithResolveRetries(5),
		WithResolveTimeout(1 * time.Second),
		WithResolveLatestTag(false),
	}
	reg = NewRegistry(ociClient, router, opts...)
	require.Equal(t, log, reg.log)
	require.Nil(t, reg.throttler)
	require.Equal(t, ociClient, reg.ociClient)
	require.Equal(t, router, reg.router)
	require.Equal(t, trans, reg.transport)
	require.Equal(t, "foobar", reg.localAddr)
	require.Equal(t, 5, reg.resolveRetries)
	require.Equal(t, 1*time.Second, reg.resolveTimeout)
	require.False(t, reg.resolveLatestTag)
}

func TestMirrorHandler(t *testing.T) {
	t.Parallel()

	badSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("foo", "bar")
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
		w.Header().Set("foo", "bar")
		if r.Method == http.MethodGet {
			//nolint:errcheck // ignore
			w.Write([]byte("hello world"))
		}
	}))
	t.Cleanup(func() {
		goodSvr.Close()
	})
	goodAddrPort := netip.MustParseAddrPort(goodSvr.Listener.Addr().String())
	unreachableAddrPort := netip.MustParseAddrPort("127.0.0.1:0")

	resolver := map[string][]netip.AddrPort{
		"no-working-peers":  {badAddrPort, unreachableAddrPort, badAddrPort},
		"first-peer":        {goodAddrPort, badAddrPort, badAddrPort},
		"first-peer-error":  {unreachableAddrPort, goodAddrPort},
		"last-peer-working": {badAddrPort, badAddrPort, goodAddrPort},
	}
	router := routing.NewMemoryRouter(resolver, netip.AddrPort{})
	reg := NewRegistry(nil, router)

	tests := []struct {
		expectedHeaders map[string][]string
		name            string
		key             string
		expectedBody    string
		expectedStatus  int
	}{
		{
			name:            "request should timeout when no peers exists",
			key:             "no-peers",
			expectedStatus:  http.StatusNotFound,
			expectedBody:    "",
			expectedHeaders: nil,
		},
		{
			name:            "request should not timeout and give 404 if all peers fail",
			key:             "no-working-peers",
			expectedStatus:  http.StatusNotFound,
			expectedBody:    "",
			expectedHeaders: nil,
		},
		{
			name:            "request should work when first peer responds",
			key:             "first-peer",
			expectedStatus:  http.StatusOK,
			expectedBody:    "hello world",
			expectedHeaders: map[string][]string{"foo": {"bar"}},
		},
		{
			name:            "second peer should respond when first gives error",
			key:             "first-peer-error",
			expectedStatus:  http.StatusOK,
			expectedBody:    "hello world",
			expectedHeaders: map[string][]string{"foo": {"bar"}},
		},
		{
			name:            "last peer should respond when two first fail",
			key:             "last-peer-working",
			expectedStatus:  http.StatusOK,
			expectedBody:    "hello world",
			expectedHeaders: map[string][]string{"foo": {"bar"}},
		},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s-%s", method, tt.name), func(t *testing.T) {
				t.Parallel()

				target := fmt.Sprintf("http://example.com/v2/foo/bar/blobs/%s", tt.key)
				rw := httptest.NewRecorder()
				req := httptest.NewRequest(method, target, nil)
				m, err := mux.NewServeMux(reg.handle)
				require.NoError(t, err)
				m.ServeHTTP(rw, req)

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

func TestRegistryHandler(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(nil, nil)
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("", "", nil)
	m, err := mux.NewServeMux(reg.handle)
	require.NoError(t, err)
	m.ServeHTTP(rw, req)

}

func TestGetClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  *http.Request
		expected string
	}{
		{
			name: "x forwarded for single",
			request: &http.Request{
				Header: http.Header{
					"X-Forwarded-For": []string{"localhost"},
				},
			},
			expected: "localhost",
		},
		{
			name: "x forwarded for multiple",
			request: &http.Request{
				Header: http.Header{
					"X-Forwarded-For": []string{"localhost,127.0.0.1"},
				},
			},
			expected: "localhost",
		},
		{
			name: "remote address",
			request: &http.Request{
				RemoteAddr: "127.0.0.1:9090",
			},
			expected: "127.0.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := getClientIP(tt.request)
			require.Equal(t, tt.expected, ip)
		})
	}
}
