package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyHeader(t *testing.T) {
	t.Parallel()

	src := http.Header{
		"foo": []string{"2", "1"},
	}
	dst := http.Header{}
	CopyHeader(dst, src)

	require.Equal(t, []string{"2", "1"}, dst.Values("foo"))
}
