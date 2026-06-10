package httpx

import (
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestEncapsulateIPv6Host(t *testing.T) {
	t.Parallel()

	res := EncapsulateIPv6Host("http://fc00:f853:ccd:e793::2:30020")
	require.Equal(t, "http://[fc00:f853:ccd:e793::2]:30020", res)
}
