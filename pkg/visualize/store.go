package visualize

import (
	"net/http"
	"net/netip"
	"sync"
	"time"
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
	Status int    `json:"status"`
}

type EventStore interface {
	RecordNoMirrors(id string)
	RecordRequest(id string, peer netip.Addr, method string, status int, mirror bool)
	FilterByDirection(rootIsSource bool) EventStore
	Graph() GraphData
	LastModified() time.Time
}

type edge struct {
	node         string
	id           string
	status       int
	rootIsSource bool
}

var _ EventStore = &MemoryStore{}

type MemoryStore struct {
	lastModified time.Time
	edgeIndex    map[string]int
	edges        []edge
	mx           sync.RWMutex
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		edges:     []edge{},
		edgeIndex: map[string]int{},
	}
}

func (m *MemoryStore) set(e edge) {
	m.mx.Lock()
	defer m.mx.Unlock()

	m.lastModified = time.Now()
	if idx, ok := m.edgeIndex[e.id]; ok {
		m.edges[idx] = e
		return
	}
	m.edges = append(m.edges, e)
	m.edgeIndex[e.id] = len(m.edges) - 1
}

func (m *MemoryStore) RecordNoMirrors(id string) {
	e := edge{
		node:         "Not Found",
		id:           id,
		rootIsSource: true,
	}
	m.set(e)
}

func (m *MemoryStore) RecordRequest(id string, peer netip.Addr, method string, status int, mirror bool) {
	if method != http.MethodGet {
		return
	}
	e := edge{
		node:         peer.String(),
		id:           id,
		status:       status,
		rootIsSource: mirror,
	}
	m.set(e)
}

func (m *MemoryStore) FilterByDirection(rootIsSource bool) EventStore { //nolint: ireturn // Have to return interface to implement interface.
	m.mx.RLock()
	defer m.mx.RUnlock()

	f := NewMemoryStore()
	f.lastModified = m.lastModified
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
		link := Link{
			ID:     edge.id,
			Source: src,
			Target: dest,
			Status: edge.status,
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

func (m *MemoryStore) LastModified() time.Time {
	m.mx.RLock()
	defer m.mx.RUnlock()

	return m.lastModified
}
