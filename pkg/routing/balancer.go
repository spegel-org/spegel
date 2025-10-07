package routing

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
)

var ErrNoNext = errors.New("no peers available for selection")

// Balancer defines how peers looked up are returned.
type Balancer interface {
	// Next returns the next peer.
	Next() (netip.AddrPort, error)
	// Size returns the amount of peers.
	Size() int
	// Add adds a peer to the balancer.
	Add(netip.AddrPort)
	// Remove removes the peer from the balancer.
	Remove(netip.AddrPort)
}

var _ Balancer = &RoundRobin{}

type RoundRobin struct {
	peers   []netip.AddrPort
	nextIdx int
	peerMx  sync.Mutex
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

func (rr *RoundRobin) Size() int {
	return len(rr.peers)
}

func (rr *RoundRobin) Add(item netip.AddrPort) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	if slices.Contains(rr.peers, item) {
		return
	}
	rr.peers = append(rr.peers, item)
}

func (rr *RoundRobin) Remove(item netip.AddrPort) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	for i, v := range rr.peers {
		if v == item {
			rr.peers = append(rr.peers[:i], rr.peers[i+1:]...)
			if rr.nextIdx > i {
				rr.nextIdx--
			} else if rr.nextIdx >= len(rr.peers) {
				rr.nextIdx = 0
			}
			return
		}
	}
}

func (rr *RoundRobin) Next() (netip.AddrPort, error) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	if len(rr.peers) == 0 {
		return netip.AddrPort{}, ErrNoNext
	}
	item := rr.peers[rr.nextIdx]
	rr.nextIdx = (rr.nextIdx + 1) % len(rr.peers)
	return item, nil
}

var _ Balancer = &ClosableBalancer{}

type ClosableBalancer struct {
	Balancer
	closeCtx  context.Context
	closeFunc context.CancelFunc
	waiters   []chan any
	waitersMx sync.Mutex
}

func NewClosableBalancer(balancer Balancer) *ClosableBalancer {
	closeCtx, closeFunc := context.WithCancel(context.Background())
	return &ClosableBalancer{
		Balancer:  balancer,
		closeCtx:  closeCtx,
		closeFunc: closeFunc,
	}
}

func (cb *ClosableBalancer) Add(item netip.AddrPort) {
	cb.Balancer.Add(item)

	cb.waitersMx.Lock()
	for _, ch := range cb.waiters {
		close(ch)
	}
	cb.waiters = nil
	cb.waitersMx.Unlock()
}

func (cb *ClosableBalancer) Next() (netip.AddrPort, error) {
	for {
		cb.waitersMx.Lock()
		peer, err := cb.Balancer.Next()
		if errors.Is(err, ErrNoNext) {
			ch := make(chan any)
			cb.waiters = append(cb.waiters, ch)
			cb.waitersMx.Unlock()

			select {
			case <-cb.closeCtx.Done():
				return netip.AddrPort{}, ErrNoNext
			case <-ch:
				continue
			}
		}
		cb.waitersMx.Unlock()
		if err != nil {
			return netip.AddrPort{}, err
		}
		return peer, nil
	}
}

func (cb *ClosableBalancer) Close() {
	cb.closeFunc()
}
