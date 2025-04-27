package cleanup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestCleanupFail(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer timeoutCancel()
	err = Wait(timeoutCtx, u.Host, 100*time.Millisecond, 3)
	require.EqualError(t, err, "context deadline exceeded")
}

func TestCleanupSucceed(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	addr := listener.Addr().String()
	err = listener.Close()
	require.NoError(t, err)
	timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer timeoutCancel()
	g, gCtx := errgroup.WithContext(timeoutCtx)
	g.Go(func() error {
		err := Run(gCtx, addr, t.TempDir())
		if err != nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		err := Wait(gCtx, addr, 100*time.Microsecond, 3)
		if err != nil {
			return err
		}
		return nil
	})

	err = g.Wait()
	require.NoError(t, err)
}
