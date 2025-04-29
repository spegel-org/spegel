package mux

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponseWriter(t *testing.T) {
	t.Parallel()

	var httpRw http.ResponseWriter = &response{}
	_, ok := httpRw.(io.ReaderFrom)
	require.True(t, ok)

	httpRw = httptest.NewRecorder()
	rw := &response{
		ResponseWriter: httpRw,
	}
	require.Equal(t, httpRw, rw.Unwrap())
	require.NoError(t, rw.Error())
	require.Equal(t, int64(0), rw.Size())
	require.Equal(t, http.StatusOK, rw.Status())

	rw = &response{
		ResponseWriter: httptest.NewRecorder(),
	}
	rw.WriteHeader(http.StatusNotFound)
	require.True(t, rw.writtenHeader)
	require.Equal(t, http.StatusNotFound, rw.Status())
	rw.WriteHeader(http.StatusBadGateway)
	require.Equal(t, http.StatusNotFound, rw.Status())
	_, err := rw.Write([]byte("foo"))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rw.Status())

	rw = &response{
		ResponseWriter: httptest.NewRecorder(),
	}
	err = errors.New("some server error")
	rw.WriteError(http.StatusInternalServerError, err)
	require.Equal(t, err, rw.Error())
	require.Equal(t, http.StatusInternalServerError, rw.Status())

	rw = &response{
		ResponseWriter: httptest.NewRecorder(),
	}
	first := "hello world"
	n, err := rw.Write([]byte(first))
	require.Equal(t, http.StatusOK, rw.Status())
	require.NoError(t, err)
	require.Equal(t, len(first), n)
	require.Equal(t, int64(len(first)), rw.Size())
	second := "foo bar"
	n, err = rw.Write([]byte(second))
	require.NoError(t, err)
	require.Equal(t, len(second), n)
	require.Equal(t, int64(len(first)+len(second)), rw.Size())

	rw = &response{
		ResponseWriter: httptest.NewRecorder(),
	}
	r := strings.NewReader("reader")
	readFromN, err := rw.ReadFrom(r)
	require.NoError(t, err)
	require.Equal(t, r.Size(), readFromN)
	require.Equal(t, r.Size(), rw.Size())

	rw = &response{
		ResponseWriter: httptest.NewRecorder(),
	}
	rw.SetHandler("foo")
	require.Equal(t, "foo", rw.handler)
}
