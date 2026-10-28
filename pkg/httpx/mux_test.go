package httpx

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/go-openapi/testify/v2/require"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestServeMux(t *testing.T) {
	t.Parallel()

	registerer := prometheus.NewRegistry()
	RegisterMetrics(registerer)

	var logBuf bytes.Buffer
	log := funcr.NewJSON(func(s string) {
		fmt.Fprintln(&logBuf, s)
	}, funcr.Options{
		Verbosity: 2,
		RenderArgsHook: func(kvList []any) []any {
			if len(kvList) == 10 {
				// Set latecy to 0 seconds.
				kvList[7] = "0s"
			}
			return kvList
		},
	})

	m := NewServeMux(log)
	handlersCalled := []string{}
	m.HandleFunc("/exact", func(rw ResponseWriter, req *http.Request) {
		handlersCalled = append(handlersCalled, "exact")
		//nolint: errcheck // No need to check in test.
		_, _ = rw.Write([]byte("exact"))
	})
	m.HandleFunc("/prefix/", func(rw ResponseWriter, req *http.Request) {
		handlersCalled = append(handlersCalled, "prefix")
	})
	m.HandleFunc("/fail", func(rw ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	})
	paths := []string{"/prefix/", "/exact", "/exact/foo", "/prefix/bar", "/fail"}
	for _, path := range paths {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost"+path, nil)
		m.ServeHTTP(rw, req)
	}

	expectedHandlersCalled := []string{"prefix", "exact", "prefix"}
	require.SliceEqualT(t, expectedHandlersCalled, handlersCalled)

	expectedMetrics := `
# HELP http_requests_inflight The number of inflight requests being handled at the same time.
# TYPE http_requests_inflight gauge
http_requests_inflight{handler="/exact"} 0
http_requests_inflight{handler="/fail"} 0
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
http_response_size_bytes_sum{code="200",handler="/exact",method="GET"} 5
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
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="1024"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="5120"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="25600"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="128000"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="640000"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="3.2e+06"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="1.6e+07"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="8e+07"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="4e+08"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="2e+09"} 1
http_response_size_bytes_bucket{code="500",handler="/fail",method="GET",le="+Inf"} 1
http_response_size_bytes_sum{code="500",handler="/fail",method="GET"} 0
http_response_size_bytes_count{code="500",handler="/fail",method="GET"} 1
`
	err = testutil.CollectAndCompare(HttpResponseSizeHistogram, strings.NewReader(expectedMetrics))
	require.NoError(t, err)

	expectedLog := `{"logger":"","level":0,"msg":"","path":"/prefix/","status":200,"method":"GET","latency":"0s","ip":"192.0.2.1"}
{"logger":"","level":0,"msg":"","path":"/exact","status":200,"method":"GET","latency":"0s","ip":"192.0.2.1"}
{"logger":"","msg":"","error":"page not found","path":"/exact/foo","status":404,"method":"GET","ip":"192.0.2.1"}
{"logger":"","level":0,"msg":"","path":"/prefix/bar","status":200,"method":"GET","latency":"0s","ip":"192.0.2.1"}
{"logger":"","msg":"","error":null,"path":"/fail","status":500,"method":"GET","latency":"0s","ip":"192.0.2.1"}
`
	require.EqualT(t, expectedLog, logBuf.String())
}

func TestClientIP(t *testing.T) {
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
					HeaderXForwardedFor: []string{"localhost"},
				},
			},
			expected: "localhost",
		},
		{
			name: "x forwarded for multiple",
			request: &http.Request{
				Header: http.Header{
					HeaderXForwardedFor: []string{"localhost,127.0.0.1"},
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

			ip := clientIP(tt.request)
			require.EqualT(t, tt.expected, ip)
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
				require.EqualT(t, tt.expected, metricsPath)
			})
		}
	}
}
