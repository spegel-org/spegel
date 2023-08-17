package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/xenitab/spegel/internal/oci"
	"github.com/xenitab/spegel/internal/routing"
)

func TestBasic(t *testing.T) {
	tests := []struct {
		name             string
		resolveLatestTag bool
	}{
		{
			name:             "resolve latest",
			resolveLatestTag: true,
		},
		{
			name:             "do not resolve latest",
			resolveLatestTag: false,
		},
	}

	imgRefs := []string{
		"docker.io/library/ubuntu:latest@sha256:b060fffe8e1561c9c3e6dea6db487b900100fc26830b9ea2ec966c151ab4c020",
		"ghcr.io/xenitab/spegel:v0.0.9@sha256:fa32bd3bcd49a45a62cfc1b0fed6a0b63bf8af95db5bad7ec22865aee0a4b795",
		"docker.io/library/alpine@sha256:25fad2a32ad1f6f510e528448ae1ec69a28ef81916a004d3629874104f8a7f70",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imgs := []oci.Image{}
			for _, imageStr := range imgRefs {
				img, err := oci.Parse(imageStr, "")
				require.NoError(t, err)
				imgs = append(imgs, img)
			}
			ociClient := oci.NewMockClient(imgs)
			router := routing.NewMockRouter(map[string][]string{})

			ctx, cancel := context.WithCancel(context.TODO())
			go func() {
				time.Sleep(2 * time.Second)
				cancel()
			}()
			Track(ctx, ociClient, router, tt.resolveLatestTag)

			for _, img := range imgs {
				peers, ok := router.LookupKey(img.Digest.String())
				require.True(t, ok)
				require.Len(t, peers, 1)
				tagName, ok := img.TagName()
				if !ok {
					continue
				}
				peers, ok = router.LookupKey(tagName)
				if img.IsLatestTag() && !tt.resolveLatestTag {
					require.False(t, ok)
					continue
				}
				require.True(t, ok)
				require.Len(t, peers, 1)
			}
		})
	}
}
