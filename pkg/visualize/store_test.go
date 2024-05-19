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
	store.RecordRequest("one", netip.MustParseAddr("127.0.0.1"), http.MethodGet, http.StatusOK, true)
	store.RecordRequest("two", netip.MustParseAddr("127.0.0.1"), http.MethodGet, http.StatusNotFound, true)
	store.RecordRequest("three", netip.MustParseAddr("10.0.0.0"), http.MethodGet, http.StatusOK, false)

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
					Status: http.StatusOK,
				},
				{
					ID:     "two",
					Source: "self",
					Target: "127.0.0.1",
					Status: http.StatusNotFound,
				},
				{
					ID:     "three",
					Source: "10.0.0.0",
					Target: "self",
					Status: http.StatusOK,
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
					Status: http.StatusOK,
				},
				{
					ID:     "two",
					Source: "self",
					Target: "127.0.0.1",
					Status: http.StatusNotFound,
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
					Status: http.StatusOK,
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
