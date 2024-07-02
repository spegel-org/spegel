package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubernetesBootstra(t *testing.T) {
	t.Parallel()

	addr := "/ip4/10.244.1.2/tcp/5001"
	peerID := "12D3KooWEkFzmb2PhhgxZv4ubV3aPfnTAhAmhmfTGuwvH4MHyPTV"
	id := addr + "/p2p/" + peerID

	cs := fake.NewSimpleClientset()
	bs := NewKubernetesBootstrapper(cs, "spegel", "leader")

	ctx, cancel := context.WithCancel(context.Background())
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return bs.Run(gCtx, id)
	})

	peerInfo, err := bs.Get()
	require.NoError(t, err)
	require.Len(t, peerInfo.Addrs, 1)
	require.Equal(t, addr, peerInfo.Addrs[0].String())
	require.Equal(t, peerID, peerInfo.ID.String())

	cancel()
	err = g.Wait()
	require.NoError(t, err)
}

func TestHTTPBootstrap(t *testing.T) {
	t.Parallel()

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
