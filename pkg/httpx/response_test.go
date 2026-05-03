package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestResponseWriter(t *testing.T) {
	t.Parallel()

	var httpRw http.ResponseWriter = &response{}
	_, ok := httpRw.(io.ReaderFrom)
	require.TrueT(t, ok)

	rw, rec := NewRecorder()
	//nolint: errcheck // No need to check unwrap.
	require.Equal(t, rec, rw.(*response).Unwrap())
	require.NoError(t, rw.Error())
	require.EqualT(t, int64(0), rw.Size())
	require.EqualT(t, http.StatusOK, rw.Status())

	rw, _ = NewRecorder()
	rw.WriteHeader(http.StatusNotFound)
	//nolint: errcheck // No need to check unwrap.
	require.TrueT(t, rw.(*response).wroteHeader)
	require.EqualT(t, http.StatusNotFound, rw.Status())
	rw.WriteHeader(http.StatusBadGateway)
	require.EqualT(t, http.StatusNotFound, rw.Status())
	_, err := rw.Write([]byte("foo"))
	require.NoError(t, err)
	require.EqualT(t, http.StatusNotFound, rw.Status())

	rw, _ = NewRecorder()
	first := "hello world"
	n, err := rw.Write([]byte(first))
	require.EqualT(t, http.StatusOK, rw.Status())
	require.NoError(t, err)
	require.EqualT(t, len(first), n)
	require.EqualT(t, int64(len(first)), rw.Size())
	second := "foo bar"
	n, err = rw.Write([]byte(second))
	require.NoError(t, err)
	require.EqualT(t, len(second), n)
	require.EqualT(t, int64(len(first)+len(second)), rw.Size())

	rw, _ = NewRecorder()
	r := strings.NewReader("reader")
	//nolint: errcheck // No need to check unwrap.
	readFromN, err := rw.(*response).ReadFrom(r)
	require.NoError(t, err)
	require.EqualT(t, r.Size(), readFromN)
	require.EqualT(t, r.Size(), rw.Size())

	rw, _ = NewRecorder()
	rw.SetAttrs("foo", "bar")
	//nolint: errcheck // No need to check unwrap.
	require.MapEqualT(t, map[string]any{"foo": "bar"}, rw.(*response).attrs)
}

func TestResponseWriterError(t *testing.T) {
	t.Parallel()

	//nolint: govet // Prioritize readability in tests.
	tests := []struct {
		err             error
		expectedBody    string
		expectedHeaders http.Header
	}{
		{
			err: errors.New("some server error"),
			expectedHeaders: http.Header{
				HeaderContentLength: {"0"},
			},
		},
		{
			err:          NewBasicResponseError("Hello World"),
			expectedBody: "Hello World",
			expectedHeaders: http.Header{
				HeaderContentType:   {ContentTypeText},
				HeaderContentLength: {"11"},
			},
		},
	}
	for _, tt := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s - %s", method, tt.err.Error()), func(t *testing.T) {
				t.Parallel()

				rec := httptest.NewRecorder()
				rw := &response{
					ResponseWriter: rec,
					method:         method,
				}
				rw.WriteError(http.StatusInternalServerError, tt.err)
				require.Equal(t, tt.err, rw.Error())
				require.EqualT(t, http.StatusInternalServerError, rw.Status())
				require.Equal(t, tt.expectedHeaders, rec.Header())
				if method != http.MethodHead {
					require.EqualT(t, tt.expectedBody, rec.Body.String())
				}
			})
		}
	}
}
