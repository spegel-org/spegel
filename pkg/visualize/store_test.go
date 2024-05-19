package visualize

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryStore(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	store.RecordRequest("one", netip.MustParseAddr("127.0.0.1"), http.StatusOK, true)
	store.RecordRequest("two", netip.MustParseAddr("127.0.0.1"), http.StatusNotFound, true)
	store.RecordRequest("three", netip.MustParseAddr("10.0.0.0"), http.StatusOK, false)

	tests := []struct {
		name          string
		store         EventStore
		expectedNodes []Node
		expectedLinks []Link
	}{
		{
			name:  "no filter",
			store: store,
			expectedNodes: []Node{
				{
					ID: "self",
				},
				{
					ID: "127.0.0.1",
				},
				{
					ID: "10.0.0.0",
				},
			},
			expectedLinks: []Link{
				{
					ID:     "one",
					Source: "self",
					Target: "127.0.0.1",
					Color:  "green",
				},
				{
					ID:     "two",
					Source: "self",
					Target: "127.0.0.1",
					Color:  "red",
				},
				{
					ID:     "three",
					Source: "10.0.0.0",
					Target: "self",
					Color:  "green",
				},
			},
		},
		{
			name:  "only from root",
			store: store.FilterByDirection(true),
			expectedNodes: []Node{
				{
					ID: "self",
				},
				{
					ID: "127.0.0.1",
				},
			},
			expectedLinks: []Link{
				{
					ID:     "one",
					Source: "self",
					Target: "127.0.0.1",
					Color:  "green",
				},
				{
					ID:     "two",
					Source: "self",
					Target: "127.0.0.1",
					Color:  "red",
				},
			},
		},
		{
			name:  "only to root",
			store: store.FilterByDirection(false),
			expectedNodes: []Node{
				{
					ID: "self",
				},
				{
					ID: "10.0.0.0",
				},
			},
			expectedLinks: []Link{
				{
					ID:     "three",
					Source: "10.0.0.0",
					Target: "self",
					Color:  "green",
				},
			},
		},
		{
			name:  "filter links",
			store: store.FilterById([]string{"three"}),
			expectedNodes: []Node{
				{
					ID: "self",
				},
				{
					ID: "10.0.0.0",
				},
			},
			expectedLinks: []Link{
				{
					ID:     "three",
					Source: "10.0.0.0",
					Target: "self",
					Color:  "green",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gd := tt.store.Graph()
			require.ElementsMatch(t, tt.expectedNodes, gd.Nodes)
			require.ElementsMatch(t, tt.expectedLinks, gd.Links)
		})
	}
}
