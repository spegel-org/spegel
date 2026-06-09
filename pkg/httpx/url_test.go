package httpx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncapsulateIPv6Host(t *testing.T) {
	t.Parallel()

	res := EncapsulateIPv6Host("http://fc00:f853:ccd:e793::2:30020")
	require.Equal(t, "http://[fc00:f853:ccd:e793::2]:30020", res)
}
