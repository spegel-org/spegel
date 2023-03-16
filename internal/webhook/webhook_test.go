package webhook

import (
	"testing"

	"github.com/containerd/containerd/reference"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestShouldPatch(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		expected bool
	}{
		{
			name:     "identical image",
			image:    "ghcr.io/xenitab/spegel:v0.0.5",
			expected: true,
		},
		{
			name:     "different registry",
			image:    "docker.io/xenitab/spegel:v0.0.5",
			expected: true,
		},
		{
			name:     "wrong version",
			image:    "ghcr.io/xenitab/spegel:v0.0.4",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := reference.Parse("ghcr.io/xenitab/spegel:v0.0.5")
			require.NoError(t, err)
			pod := corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Image: tt.image,
						},
					},
				},
			}
			ok := shouldPatch(&pod, ref)
			require.Equal(t, tt.expected, ok)
		})
	}
}

func TestShouldPatchTooManyContainers(t *testing.T) {
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{},
				{},
			},
		},
	}
	ok := shouldPatch(&pod, reference.Spec{})
	require.False(t, ok)
}