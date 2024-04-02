package registry

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/pkg/routing"
)

type TestResponseRecorder struct {
	*httptest.ResponseRecorder
	closeChannel chan bool
}

func (r *TestResponseRecorder) CloseNotify() <-chan bool {
	return r.closeChannel
}

//nolint:unused // ignore
func (r *TestResponseRecorder) closeClient() {
	r.closeChannel <- true
}

func CreateTestResponseRecorder() *TestResponseRecorder {
	return &TestResponseRecorder{
		httptest.NewRecorder(),
		make(chan bool, 1),
	}
}

func TestMirrorHandler(t *testing.T) {
	badSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("foo", "bar")
		if r.Method == http.MethodGet {
			//nolint:errcheck // ignore
			w.Write([]byte("hello world"))
		}
	}))
	defer badSvr.Close()
	badAddrPort := netip.MustParseAddrPort(badSvr.Listener.Addr().String())
	goodSvr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("foo", "bar")
		if r.Method == http.MethodGet {
			//nolint:errcheck // ignore
			w.Write([]byte("hello world"))
		}
	}))
	defer goodSvr.Close()
	goodAddrPort := netip.MustParseAddrPort(goodSvr.Listener.Addr().String())
	unreachableAddrPort := netip.MustParseAddrPort("127.0.0.1:0")

	resolver := map[string][]netip.AddrPort{
		"no-working-peers":  {badAddrPort, unreachableAddrPort, badAddrPort},
		"first-peer":        {goodAddrPort, badAddrPort, badAddrPort},
		"first-peer-error":  {unreachableAddrPort, goodAddrPort},
		"last-peer-working": {badAddrPort, badAddrPort, goodAddrPort},
	}
	router := routing.NewMockRouter(resolver, netip.AddrPort{})
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
			name:            "request should not timeout and give 500 if all peers fail",
			key:             "no-working-peers",
			expectedStatus:  http.StatusInternalServerError,
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
			t.Run(tt.name, func(t *testing.T) {
				rw := CreateTestResponseRecorder()
				c, _ := gin.CreateTestContext(rw)
				target := fmt.Sprintf("http://example.com/%s", tt.key)
				c.Request = httptest.NewRequest(method, target, nil)
				reg.handleMirror(c, tt.key)

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
