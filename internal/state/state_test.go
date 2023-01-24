package state

import (
	"context"
	"testing"

	"github.com/containerd/containerd"
	"github.com/stretchr/testify/require"
)

func TestImageLayers(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		expected []string
	}{
		{
			name:  "only tag",
			image: "docker.io/library/alpine:3.17.1",
			expected: []string{
				"docker.io/library/alpine:3.17.1",
				"sha256:f271e74b17ced29b915d351685fd4644785c6d1559dd1f2d4189a5e851ef753a",
				"sha256:042a816809aac8d0f7d7cacac7965782ee2ecac3f21bcf9f24b1de1a7387b769",
				"sha256:8921db27df2831fa6eaa85321205a2470c669b855f3ec95d5a3c2b46de0442c9",
				"sha256:93d5a28ff72d288d69b5997b8ba47396d2cbb62a72b5d87cd3351094b5d578a0",
			},
		},
		{
			name:  "only digest",
			image: "docker.io/library/alpine@sha256:f271e74b17ced29b915d351685fd4644785c6d1559dd1f2d4189a5e851ef753a",
			expected: []string{
				"sha256:f271e74b17ced29b915d351685fd4644785c6d1559dd1f2d4189a5e851ef753a",
				"sha256:042a816809aac8d0f7d7cacac7965782ee2ecac3f21bcf9f24b1de1a7387b769",
				"sha256:8921db27df2831fa6eaa85321205a2470c669b855f3ec95d5a3c2b46de0442c9",
				"sha256:93d5a28ff72d288d69b5997b8ba47396d2cbb62a72b5d87cd3351094b5d578a0",
			},
		},
		{
			name:  "tag and digest",
			image: "docker.io/library/alpine:3.17.1@sha256:f271e74b17ced29b915d351685fd4644785c6d1559dd1f2d4189a5e851ef753a",
			expected: []string{
				"docker.io/library/alpine:3.17.1",
				"sha256:f271e74b17ced29b915d351685fd4644785c6d1559dd1f2d4189a5e851ef753a",
				"sha256:042a816809aac8d0f7d7cacac7965782ee2ecac3f21bcf9f24b1de1a7387b769",
				"sha256:8921db27df2831fa6eaa85321205a2470c669b855f3ec95d5a3c2b46de0442c9",
				"sha256:93d5a28ff72d288d69b5997b8ba47396d2cbb62a72b5d87cd3351094b5d578a0",
			},
		},
	}

	containerdClient, err := containerd.New("/run/containerd/containerd.sock", containerd.WithDefaultNamespace("default"))
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := containerdClient.GetImage(context.TODO(), tt.image)
			require.NoError(t, err)
			layers, err := imageLayers(context.TODO(), containerdClient, img)
			require.NoError(t, err)
			require.Equal(t, tt.expected, layers)
		})
	}
}