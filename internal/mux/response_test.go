package mux

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponseWriter(t *testing.T) {
	var rw http.ResponseWriter = &response{}
	_, ok := rw.(io.ReaderFrom)
	require.True(t, ok)
}
