package oci

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xenitab/spegel/internal/utils"
)

func TestCreateFilter(t *testing.T) {
	tests := []struct {
		name                string
		registries          []string
		imageFilter         string
		expectedListFilter  string
		expectedEventFilter string
	}{
		{
			name:                "only registries",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			expectedListFilter:  `name~="docker.io|gcr.io"`,
			expectedEventFilter: `topic~="/images/create|/images/update",event.name~="docker.io|gcr.io"`,
		},
		{
			name:                "additional image filtes",
			registries:          []string{"https://docker.io", "https://gcr.io"},
			imageFilter:         "xenitab/spegel",
			expectedListFilter:  `name~="docker.io|gcr.io|xenitab/spegel"`,
			expectedEventFilter: `topic~="/images/create|/images/update",event.name~="docker.io|gcr.io|xenitab/spegel"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listFilter, eventFilter := createFilters(utils.StringListToUrlList(t, tt.registries), tt.imageFilter)
			require.Equal(t, listFilter, tt.expectedListFilter)
			require.Equal(t, eventFilter, tt.expectedEventFilter)
		})
	}
}
