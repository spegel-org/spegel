package mux

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServeMux(t *testing.T) {
	t.Parallel()

	m, err := NewServeMux(nil)
	require.Nil(t, m)
	require.EqualError(t, err, "handler cannot be nil")

	handlerCalled := false
	h := func(rw ResponseWriter, req *http.Request) {
		handlerCalled = true
	}
	m, err = NewServeMux(h)
	require.NoError(t, err)
	m.ServeHTTP(nil, nil)
	require.True(t, handlerCalled)
}
