package mux

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestServeMux(t *testing.T) {
	t.Parallel()

	registerer := prometheus.NewRegistry()
	RegisterMetrics(registerer)

	m := NewServeMux(logr.Discard())
	handlersCalled := []string{}
	m.Handle("/exact", func(rw ResponseWriter, req *http.Request) {
		handlersCalled = append(handlersCalled, "exact")
	})
	m.Handle("/prefix/", func(rw ResponseWriter, req *http.Request) {
		handlersCalled = append(handlersCalled, "prefix")
	})
	paths := []string{"/prefix/", "/exact", "/exact/foo", "/prefix/bar"}
	for _, path := range paths {
		rw := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		m.ServeHTTP(rw, req)
	}

	expectedHandlersCalled := []string{"prefix", "exact", "prefix"}
	require.Equal(t, expectedHandlersCalled, handlersCalled)

	expectedMetrics := `
# HELP http_requests_inflight The number of inflight requests being handled at the same time.
# TYPE http_requests_inflight gauge
http_requests_inflight{handler="/exact"} 0
http_requests_inflight{handler="/prefix/*"} 0
`
	err := testutil.CollectAndCompare(HttpRequestsInflight, strings.NewReader(expectedMetrics))
	require.NoError(t, err)

	expectedMetrics = `
# HELP http_response_size_bytes The size of the HTTP responses.
# TYPE http_response_size_bytes histogram
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="1024"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="5120"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="25600"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="128000"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="640000"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="3.2e+06"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="1.6e+07"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="8e+07"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="4e+08"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="2e+09"} 1
http_response_size_bytes_bucket{code="200",handler="/exact",method="GET",le="+Inf"} 1
http_response_size_bytes_sum{code="200",handler="/exact",method="GET"} 0
http_response_size_bytes_count{code="200",handler="/exact",method="GET"} 1
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="1024"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="5120"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="25600"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="128000"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="640000"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="3.2e+06"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="1.6e+07"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="8e+07"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="4e+08"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="2e+09"} 2
http_response_size_bytes_bucket{code="200",handler="/prefix/*",method="GET",le="+Inf"} 2
http_response_size_bytes_sum{code="200",handler="/prefix/*",method="GET"} 0
http_response_size_bytes_count{code="200",handler="/prefix/*",method="GET"} 2
`
	err = testutil.CollectAndCompare(HttpResponseSizeHistogram, strings.NewReader(expectedMetrics))
	require.NoError(t, err)
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

			ip := GetClientIP(tt.request)
			require.Equal(t, tt.expected, ip)
		})
	}
}

func TestMetricsFriendlyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern  string
		expected string
	}{
		{
			pattern:  "/",
			expected: "/*",
		},
		{
			pattern:  "/exact",
			expected: "/exact",
		},
		{
			pattern:  "/prefix/",
			expected: "/prefix/*",
		},
		{
			pattern:  "/chats/{id}/message/{index}",
			expected: "/chats/{id}/message/{index}",
		},
	}
	for _, method := range []string{"", "GET ", "HEAD "} {
		for _, tt := range tests {
			t.Run(tt.pattern, func(t *testing.T) {
				t.Parallel()

				metricsPath := metricsFriendlyPath(method + tt.pattern)
				require.Equal(t, tt.expected, metricsPath)
			})
		}
	}
}
