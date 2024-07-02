package routing

import (
	"net/netip"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestListenMultiaddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		expected []string
	}{
		{
			name:     "listen address type not specified",
			addr:     ":9090",
			expected: []string{"/ip6/::/tcp/9090", "/ip4/0.0.0.0/tcp/9090"},
		},
		{
			name:     "ipv4 only",
			addr:     "0.0.0.0:9090",
			expected: []string{"/ip4/0.0.0.0/tcp/9090"},
		},
		{
			name:     "ipv6 only",
			addr:     "[::]:9090",
			expected: []string{"/ip6/::/tcp/9090"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			multiAddrs, err := listenMultiaddrs(tt.addr)
			require.NoError(t, err)
			require.Equal(t, len(tt.expected), len(multiAddrs))
			for i, e := range tt.expected {
				require.Equal(t, e, multiAddrs[i].String())
			}
		})
	}
}

func TestIPInMultiaddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ma       string
		expected netip.Addr
		name     string
	}{
		{
			name:     "ipv4",
			ma:       "/ip4/10.244.1.2/tcp/5001",
			expected: netip.MustParseAddr("10.244.1.2"),
		},
		{
			name:     "ipv6",
			ma:       "/ip6/0:0:0:0:0:ffff:0af4:0102/tcp/5001",
			expected: netip.MustParseAddr("::ffff:10.244.1.2"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			multiAddr, err := ma.NewMultiaddr(tt.ma)
			require.NoError(t, err)
			v, err := ipInMultiaddr(multiAddr)
			require.NoError(t, err)
			require.Equal(t, tt.expected, v)
		})
	}
}

func TestIsIp6(t *testing.T) {
	t.Parallel()

	m, err := ma.NewMultiaddr("/ip6/::")
	require.NoError(t, err)
	require.True(t, isIp6(m))
	m, err = ma.NewMultiaddr("/ip4/0.0.0.0")
	require.NoError(t, err)
	require.False(t, isIp6(m))
}

func TestCreateCid(t *testing.T) {
	t.Parallel()

	c, err := createCid("foobar")
	require.NoError(t, err)
	require.Equal(t, "bafkreigdvoh7cnza5cwzar65hfdgwpejotszfqx2ha6uuolaofgk54ge6i", c.String())
}
