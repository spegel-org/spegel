package ptr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTo(t *testing.T) {
	t.Parallel()

	v := "hello world"
	p := To(v)
	require.Equal(t, v, *p)
	require.Equal(t, &v, p)
}
