package web

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeb(t *testing.T) {
	t.Parallel()

	w, err := NewWeb(nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, w.tmpls)
}
