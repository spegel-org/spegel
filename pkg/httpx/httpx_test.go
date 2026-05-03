package httpx

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-openapi/testify/v2/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestBaseClient(t *testing.T) {
	t.Parallel()

	c := BaseClient()
	require.EqualT(t, 10*time.Second, c.Timeout)
	_, ok := c.Transport.(*http.Transport)
	require.TrueT(t, ok)
}

func TestBaseTransport(t *testing.T) {
	t.Parallel()

	BaseTransport()
}

func TestDrainAndClose(t *testing.T) {
	t.Parallel()

	buf := bytes.NewBuffer(nil)
	err := DrainAndClose(io.NopCloser(buf))
	require.NoError(t, err)
	require.Empty(t, buf.Bytes())

	buf = bytes.NewBuffer(make([]byte, MaxReadBytes))
	err = DrainAndClose(io.NopCloser(buf))
	require.NoError(t, err)
	require.Empty(t, buf.Bytes())

	buf = bytes.NewBuffer(make([]byte, MaxReadBytes+10))
	err = DrainAndClose(io.NopCloser(buf))
	require.EqualError(t, err, "reader has more data than max read bytes")
	require.Len(t, buf.Bytes(), 9)
}
