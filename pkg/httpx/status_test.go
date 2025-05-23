package httpx

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		contentType   string
		body          string
		expectedError string
		requestMethod string
		expectedCodes []int
		statusCode    int
	}{
		{
			name:          "status code matches one of expected",
			contentType:   "text/plain",
			body:          "Hello World",
			statusCode:    http.StatusOK,
			expectedCodes: []int{http.StatusNotFound, http.StatusOK},
			requestMethod: http.MethodGet,
			expectedError: "",
		},
		{
			name:          "no expected status codes",
			contentType:   "text/plain",
			statusCode:    http.StatusOK,
			expectedCodes: []int{},
			expectedError: "expected codes cannot be empty",
		},
		{
			name:          "wrong code with text content and GET request",
			contentType:   "text/plain",
			body:          "Hello World",
			statusCode:    http.StatusNotFound,
			expectedCodes: []int{http.StatusOK},
			requestMethod: http.MethodGet,
			expectedError: "expected one of the following statuses [200 OK], but received 404 Not Found: Hello World",
		},
		{
			name:          "wrong code with text content and HEAD request",
			contentType:   "text/plain",
			body:          "Hello World",
			statusCode:    http.StatusNotFound,
			expectedCodes: []int{http.StatusOK, http.StatusPartialContent},
			requestMethod: http.MethodHead,
			expectedError: "expected one of the following statuses [200 OK, 206 Partial Content], but received 404 Not Found",
		},
		{
			name:          "wrong code with text content and GET request but octet stream",
			contentType:   "application/octet-stream",
			body:          "Hello World",
			statusCode:    http.StatusNotFound,
			expectedCodes: []int{http.StatusOK},
			requestMethod: http.MethodGet,
			expectedError: "expected one of the following statuses [200 OK], but received 404 Not Found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			rec.WriteHeader(tt.statusCode)
			rec.Header().Set(HeaderContentType, tt.contentType)
			rec.Body = bytes.NewBufferString(tt.body)

			resp := &http.Response{
				StatusCode: tt.statusCode,
				Status:     http.StatusText(tt.statusCode),
				Header:     rec.Header(),
				Body:       io.NopCloser(rec.Body),
				Request: &http.Request{
					Method: tt.requestMethod,
				},
			}

			err := CheckResponseStatus(resp, tt.expectedCodes...)
			if tt.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.expectedError)
			}
		})
	}
}
