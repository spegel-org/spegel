package ptr

import (
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestTo(t *testing.T) {
	t.Parallel()

	v := "hello world"
	p := To(v)
	require.EqualT(t, v, *p)
	require.Equal(t, &v, p)
}
