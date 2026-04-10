package routing

import (
	"net/netip"
	"sync"
	"time"
)

// Peer represents a host reachable at one or more addresses.
type Peer struct {
	Host      string
	Addresses []netip.Addr
	Metadata  PeerMetadata
}

// PeerMetadata contains additional information for the peer.
type PeerMetadata struct {
	RegistryPort uint16
}

// Iterator maintains track of peers for a given lookup.
type Iterator struct {
	acquired    map[string]any
	peers       map[string]Peer
	usage       map[string]int
	exhaustedCh chan any
	readyCh     chan any
	lastUpdate  time.Time
	mx          sync.RWMutex
	closed      bool
}

func NewIterator() *Iterator {
	return &Iterator{
		acquired:    map[string]any{},
		peers:       map[string]Peer{},
		usage:       map[string]int{},
		exhaustedCh: make(chan any),
		readyCh:     make(chan any),
		lastUpdate:  time.Now(),
		closed:      false,
	}
}

// TimeSinceUpdate returns the duration since the last update.
func (it *Iterator) TimeSinceUpdate() time.Duration {
	it.mx.RLock()
	defer it.mx.RUnlock()

	return time.Since(it.lastUpdate)
}

// Count returns the amount of peers in the iterator.
func (it *Iterator) Count() int {
	it.mx.RLock()
	defer it.mx.RUnlock()

	return len(it.peers)
}

// Add adds a peer to the iterator.
// If iterator was previously empty the iterator will become ready.
func (it *Iterator) Add(peer Peer) {
	it.mx.Lock()
	defer it.mx.Unlock()

	peerCount := len(it.peers)
	it.peers[peer.Host] = peer
	if len(it.peers) != peerCount && len(it.peers) == 1 {
		close(it.readyCh)
		if it.closed {
			it.exhaustedCh = make(chan any)
		}
	}
}

// Remove removes a peer from the iterator.
// If the iterator is empty the iterator will become not ready.
func (it *Iterator) Remove(peer Peer) {
	it.mx.Lock()
	defer it.mx.Unlock()

	peerCount := len(it.peers)
	delete(it.peers, peer.Host)
	delete(it.acquired, peer.Host)
	delete(it.usage, peer.Host)
	if len(it.peers) != peerCount && len(it.peers) == 0 {
		it.readyCh = make(chan any)
		if it.closed {
			close(it.exhaustedCh)
		}
	}
}

// Acquire gets the least used peer in the iterator.
// If all peers have been acquired the iterator becomes not ready.
func (it *Iterator) Acquire() (Peer, bool) {
	it.mx.Lock()
	defer it.mx.Unlock()

	// If empty or all peers have been acquired.
	if len(it.peers) == 0 || len(it.peers) == len(it.acquired) {
		return Peer{}, false
	}

	// Select the least used peer.
	peer := Peer{}
	count := -1
	for _, v := range it.peers {
		if count == -1 || it.usage[v.Host] < count {
			peer = v
			count = it.usage[v.Host]
		}

	}
	it.usage[peer.Host] += 1
	it.acquired[peer.Host] = nil

	// No longer ready if all peers have been acquired.
	if len(it.peers) == len(it.acquired) {
		it.readyCh = make(chan any)
	}

	return peer, true
}

// Release returns a peer to the iterator to be used again.
// If the iterator is not ready it will becom ready again.
func (it *Iterator) Release(peer Peer) {
	it.mx.Lock()
	defer it.mx.Unlock()

	acquiredCount := len(it.acquired)
	delete(it.acquired, peer.Host)
	if len(it.acquired) != acquiredCount && len(it.acquired) == len(it.peers)-1 {
		close(it.readyCh)
	}
}

// Open indicates that the iterator is updating.
// Will always reset the exhausted state.
func (it *Iterator) Open() {
	it.mx.Lock()
	defer it.mx.Unlock()

	if !it.closed {
		return
	}
	it.lastUpdate = time.Now()
	it.closed = false
	it.exhaustedCh = make(chan any)
}

// Close indicates that the iterator is not updating.
// If there are not peers the iterator becomes exhausted.
func (it *Iterator) Close() {
	it.mx.Lock()
	defer it.mx.Unlock()

	if it.closed {
		return
	}

	it.closed = true
	if len(it.peers) == 0 {
		close(it.exhaustedCh)
	}
}

// Ready returns a channel that closes when the iterator is ready.
func (it *Iterator) Ready() chan any {
	it.mx.RLock()
	defer it.mx.RUnlock()

	return it.readyCh
}

// Exhausted returns a channel that closes when the iterator is exhausted.
func (it *Iterator) Exhausted() chan any {
	it.mx.RLock()
	defer it.mx.RUnlock()

	return it.exhaustedCh
}
