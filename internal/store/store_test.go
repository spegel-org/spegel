package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/cilium/fake"
	"github.com/stretchr/testify/require"

	"github.com/xenitab/spegel/internal/discover"
)

func TestStore(t *testing.T) {
	layers := []string{"foo", "bar", "baz"}
	d := discover.NewMock([]string{fake.IP(fake.WithIPv4())})
	peers, err := d.GetPeers(context.TODO())

	for _, st := range []string{"olric", "redis"} {
		var s Store
		switch st {
		case "olric":
			s, err = NewOlricLocalStore(context.TODO(), d, peers[0])
			require.NoError(t, err)
			go s.Start()
			err = s.Ready()
			require.NoError(t, err)
		case "redis":
			mr := miniredis.RunT(t)
			defer mr.Close()
			s, err = NewRedisStore(peers[0], d, mr.Addr())
			require.NoError(t, err)
		}

		err = s.Set(context.TODO(), layers)
		require.NoError(t, err)
		data, err := s.Dump(context.TODO())
		require.NoError(t, err)
		for _, d := range data {
			require.Contains(t, []string{fmt.Sprintf("layer:%s:foo", peers[0]), fmt.Sprintf("layer:%s:bar", peers[0]), fmt.Sprintf("layer:%s:baz", peers[0])}, d)
		}
		err = s.Remove(context.TODO(), layers)
		require.NoError(t, err)
		data, err = s.Dump(context.TODO())
		require.NoError(t, err)
		require.Empty(t, data)
	}

}
