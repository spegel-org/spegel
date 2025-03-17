package registry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/mux"
	"github.com/spegel-org/spegel/pkg/routing"
)

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

			reg := NewRegistry(nil, nil, WithBasicAuth(tt.username, tt.password))
			rw := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://localhost/v2", nil)
			req.SetBasicAuth(tt.reqUsername, tt.reqPassword)
			m, err := mux.NewServeMux(reg.handle)
			require.NoError(t, err)
			m.ServeHTTP(rw, req)

			require.Equal(t, tt.expected, rw.Result().StatusCode)
		})
	}
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
			key:             "sha256:c3e30fbcf3b231356a1efbd30a8ccec75134a7a8b45217ede97f4ff483540b04",
			expectedStatus:  http.StatusNotFound,
			expectedBody:    "",
			expectedHeaders: nil,
		},
		{
			name:            "request should work when first peer responds",
			key:             "sha256:3b8a55c543ccc7ae01c47b1d35af5826a6439a9b91ab0ca96de9967759279896",
			expectedStatus:  http.StatusOK,
			expectedBody:    "hello world",
			expectedHeaders: map[string][]string{"foo": {"bar"}},
		},
		{
			name:            "second peer should respond when first gives error",
			key:             "sha256:a0daab85ec30e2809a38c32fa676515aba22f481c56fda28637ae964ff398e3d",
			expectedStatus:  http.StatusOK,
			expectedBody:    "hello world",
			expectedHeaders: map[string][]string{"foo": {"bar"}},
		},
		{
			name:            "last peer should respond when two first fail",
			key:             "sha256:11242d2a347bf8ab30b9f92d5ca219bbbedf95df5a8b74631194561497c1fae8",
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
