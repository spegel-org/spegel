package cleanup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-openapi/testify/v2/require"

	"github.com/kvick-org/pkg/errgroup"
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

	listenCfg := &net.ListenConfig{}
	listener, err := listenCfg.Listen(t.Context(), "tcp", ":")
	require.NoError(t, err)
	addr := listener.Addr().String()
	err = listener.Close()
	require.NoError(t, err)
	timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer timeoutCancel()
	group := errgroup.WithContext(timeoutCtx)
	group.Go(func(ctx context.Context) error {
		err := Run(ctx, addr, t.TempDir())
		if err != nil {
			return err
		}
		return nil
	})
	group.Go(func(ctx context.Context) error {
		err := Wait(ctx, addr, 100*time.Microsecond, 3)
		if err != nil {
			return err
		}
		return nil
	})

	err = group.Wait()
	require.NoError(t, err)
}
