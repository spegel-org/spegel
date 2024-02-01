package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPBootstrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	id := "/ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ"
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // ignore
		w.Write([]byte(id))
	}))
	defer svr.Close()

	bootstrapper := NewHTTPBootstrapper(":", svr.URL)
	//nolint:errcheck // ignore
	go bootstrapper.Run(ctx, id)
	addrInfo, err := bootstrapper.Get()
	require.NoError(t, err)
	require.Len(t, addrInfo.Addrs, 1)
	require.Equal(t, "/ip4/104.131.131.82/tcp/4001", addrInfo.Addrs[0].String())
	require.Equal(t, "QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ", addrInfo.ID.String())
}
