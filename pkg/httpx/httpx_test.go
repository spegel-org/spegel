package httpx

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBaseClient(t *testing.T) {
	t.Parallel()

	c := BaseClient()
	require.Equal(t, 10*time.Second, c.Timeout)
	_, ok := c.Transport.(*http.Transport)
	require.True(t, ok)
}

func TestBaseTransport(t *testing.T) {
	t.Parallel()

	BaseTransport()
}
