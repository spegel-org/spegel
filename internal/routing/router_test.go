package routing

import (
	"fmt"
	"testing"

	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

func TestCreateCid(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{
			name:  "sha256 hash",
			value: "sha256:4f380adfc10f4cd34f775ae57a17d2835385efd5251d6dfe0f246b0018fb0399",
		},
		{
			name:  "version number",
			value: "v1.0.0",
		},
	}
	for _, tt := range tests {
		c, err := createCid(tt.value)
		require.NoError(t, err)
		require.Equal(t, uint64(1), c.Version())
		prefix := c.Prefix()
		require.Equal(t, uint64(mh.SHA2_256), prefix.MhType)
		require.Equal(t, uint64(mc.Raw), prefix.Codec)

		fmt.Println(c.String())
		require.Equal(t, 1, 0)
	}
}

func BenchmarkCreateCid(b *testing.B) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "no hashing",
			input: "sha256:4f380adfc10f4cd34f775ae57a17d2835385efd5251d6dfe0f246b0018fb0399",
		},
		{
			name:  "hashing",
			input: "4f380adfc10f4cd34f775ae57a17d2835385efd5251d6dfe0f246b0018fb0399",
		},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, err := createCid(tt.input)
				require.NoError(b, err)
			}
		})
	}
}
