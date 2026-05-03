package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestHappyEyeballs(t *testing.T) {
	t.Parallel()

	// Empty address list.
	_, err := HappyEyeballs[any](t.Context(), nil, nil)
	require.Error(t, err)

	// Make request to multiple addresses.
	listenCfg := net.ListenConfig{}
	blackSrv, err := listenCfg.Listen(t.Context(), "tcp", ":0")
	require.NoError(t, err)
	defer blackSrv.Close()

	httpSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // ignore
		w.Write([]byte("Hello World"))
	}))
	httpSrv.Start()
	defer httpSrv.Close()

	addrPorts := []netip.AddrPort{
		netip.MustParseAddrPort(blackSrv.Addr().String()),
		netip.MustParseAddrPort(httpSrv.Listener.Addr().String()),
	}
	ipAddrs := []netip.Addr{}
	for _, addrPort := range addrPorts {
		ipAddrs = append(ipAddrs, addrPort.Addr())
	}

	res, err := HappyEyeballs(t.Context(), ipAddrs, func(ctx context.Context, ipAddr netip.Addr) (string, error) {
		addrPort, err := func() (netip.AddrPort, error) {
			for _, addrPort := range addrPorts {
				if addrPort.Addr() == ipAddr {
					return addrPort, nil
				}
			}
			return netip.AddrPort{}, errors.New("not found")
		}()
		if err != nil {
			return "", err
		}

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addrPort.String(), nil)
		require.NoError(t, err)
		resp, err := httpSrv.Client().Do(req)
		if err != nil {
			return "", err
		}
		err = CheckResponseStatus(resp, http.StatusOK)
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
	require.NoError(t, err)
	require.EqualT(t, "Hello World", res)
}
