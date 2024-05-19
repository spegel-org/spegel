package visualize

import (
	"net/http"
	"net/netip"
	"sync"
)

type GraphData struct {
	Nodes []Node `json:"nodes"`
	Links []Link `json:"links"`
}

type Node struct {
	ID string `json:"id"`
}

type Link struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Color  string `json:"color"`
}

type EventStore interface {
	RecordExisting(id string, registry string)
	RecordRequest(id string, peer netip.Addr, status int, mirror bool)
	FilterById(include []string) EventStore
	FilterByDirection(rootIsSource bool) EventStore
	Graph() GraphData
}

// TODO: Include blob or manifest
type edge struct {
	node         string
	id           string
	status       int
	rootIsSource bool
}

var _ EventStore = &MemoryStore{}

type MemoryStore struct {
	mx        sync.RWMutex
	edges     []edge
	edgeIndex map[string]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		edges:     []edge{},
		edgeIndex: map[string]int{},
	}
}

func (m *MemoryStore) RecordExisting(id string, registry string) {
	m.record(id, registry, http.StatusOK, true)
}

func (m *MemoryStore) RecordRequest(id string, peer netip.Addr, status int, mirror bool) {
	m.record(id, peer.String(), status, mirror)
}

func (m *MemoryStore) record(id string, node string, status int, rootIsSource bool) {
	m.mx.Lock()
	defer m.mx.Unlock()

	e := edge{
		node:         node,
		id:           id,
		status:       status,
		rootIsSource: rootIsSource,
	}
	if idx, ok := m.edgeIndex[id]; ok {
		m.edges[idx] = e
		return
	}
	m.edges = append(m.edges, e)
	m.edgeIndex[id] = len(m.edges) - 1
}

func (m *MemoryStore) FilterById(include []string) EventStore {
	m.mx.RLock()
	defer m.mx.RUnlock()

	f := NewMemoryStore()
	for _, v := range include {
		idx, ok := m.edgeIndex[v]
		if !ok {
			continue
		}
		edge := m.edges[idx]
		f.edges = append(f.edges, edge)
		f.edgeIndex[v] = len(f.edges) - 1
	}
	return f
}

func (m *MemoryStore) FilterByDirection(rootIsSource bool) EventStore {
	m.mx.RLock()
	defer m.mx.RUnlock()

	f := NewMemoryStore()
	for _, edge := range m.edges {
		if edge.rootIsSource != rootIsSource {
			continue
		}
		f.edges = append(f.edges, edge)
		f.edgeIndex[edge.id] = len(f.edges) - 1
	}
	return f
}

func (m *MemoryStore) Graph() GraphData {
	m.mx.RLock()
	defer m.mx.RUnlock()

	gd := GraphData{
		Nodes: []Node{
			{
				ID: "self",
			},
		},
		Links: []Link{},
	}
	nodeIndex := map[string]interface{}{}
	for _, edge := range m.edges {
		src := gd.Nodes[0].ID
		dest := edge.node
		if !edge.rootIsSource {
			src = edge.node
			dest = gd.Nodes[0].ID
		}
		color := linkColor(edge.status)
		link := Link{
			ID:     edge.id,
			Source: src,
			Target: dest,
			Color:  color,
		}
		gd.Links = append(gd.Links, link)

		if _, ok := nodeIndex[edge.node]; ok {
			continue
		}
		gd.Nodes = append(gd.Nodes, Node{ID: edge.node})
		nodeIndex[edge.node] = nil
	}
	return gd
}

func linkColor(status int) string {
	switch status {
	case 0:
		return "yellow"
	case http.StatusOK:
		return "green"
	default:
		return "red"
	}
}
