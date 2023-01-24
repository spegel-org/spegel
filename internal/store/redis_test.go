package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/cilium/fake"
	"github.com/stretchr/testify/require"
)

func TestRedisStore(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	layers := []string{"foo", "bar", "baz"}
	peers := NewMock([]string{fake.IP(fake.WithIPv4()), fake.IP(fake.WithIPv4())})
	pp, err := peers.GetPeers(context.TODO())
	mirrorStore, err := NewRedisStore(pp[0], peers, []string{s.Addr()})
	require.NoError(t, err)
	registryStore, err := NewRedisStore(pp[1], peers, []string{s.Addr()})
	require.NoError(t, err)

	err = registryStore.Add(context.TODO(), layers)
	require.NoError(t, err)
	ips, err := mirrorStore.Get(context.TODO(), layers[1])
	require.Len(t, ips, 1)
	require.Equal(t, pp[1], ips[0])
	data, err := mirrorStore.Dump(context.TODO())
	require.NoError(t, err)
	for _, d := range data {
		require.Contains(t, []string{fmt.Sprintf("layer:%s:foo", pp[1]), fmt.Sprintf("layer:%s:bar", pp[1]), fmt.Sprintf("layer:%s:baz", pp[1])}, d)
	}

	err = registryStore.Remove(context.TODO(), layers)
	require.NoError(t, err)
	for _, layer := range layers {
		ips, err := registryStore.Get(context.TODO(), layer)
		require.NoError(t, err)
		require.Empty(t, ips)
	}
}
