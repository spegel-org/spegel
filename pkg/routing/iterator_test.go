package routing

import (
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/internal/testutil"
)

func TestIterator(t *testing.T) {
	t.Parallel()

	iter := NewIterator()
	require.Empty(t, iter.peers)
	require.Empty(t, iter.acquired)
	require.False(t, iter.closed)
	require.Equal(t, 0, iter.Count())
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())
	_, ok := iter.Acquire()
	require.False(t, ok)

	// Open iterator should reset exhausted.
	iter.Open()
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())

	// Adding peer should make iterator ready.
	peer := Peer{Host: "foo", Addresses: nil}
	for range 3 {
		iter.Add(peer)
	}
	require.Len(t, iter.peers, 1)
	testutil.RequireChannelClosed(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())

	// Acquire and release peer.
	acquiredPeer, ok := iter.Acquire()
	require.True(t, ok)
	require.Equal(t, peer.Host, acquiredPeer.Host)
	require.Len(t, iter.peers, 1)
	require.Len(t, iter.acquired, 1)
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())
	_, ok = iter.Acquire()
	require.False(t, ok)

	iter.Release(acquiredPeer)
	require.Len(t, iter.peers, 1)
	require.Empty(t, iter.acquired)
	testutil.RequireChannelClosed(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())

	// Remove acquired peer should work.
	acquiredPeer, ok = iter.Acquire()
	require.True(t, ok)
	for range 3 {
		iter.Remove(acquiredPeer)
	}
	require.Empty(t, iter.peers)
	require.Empty(t, iter.acquired)
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())

	// Closed channel.
	iter.Close()
	require.True(t, iter.closed)
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelClosed(t, iter.Exhausted())
	iter.Add(peer)
	testutil.RequireChannelClosed(t, iter.Ready())
	testutil.RequireChannelOpen(t, iter.Exhausted())
	iter.Remove(peer)
	testutil.RequireChannelOpen(t, iter.Ready())
	testutil.RequireChannelClosed(t, iter.Exhausted())

	// Peers should be balanced.
	for i := range 3 {
		peer := Peer{Host: strconv.FormatInt(int64(i), 10)}
		for range 3 {
			iter.Add(peer)
		}
	}
	require.Len(t, iter.peers, 3)
	for range 6 {
		peer, ok := iter.Acquire()
		require.True(t, ok)
		iter.Release(peer)
	}
	for _, v := range iter.usage {
		require.Equal(t, 2, v)
	}

	// All peers should be removed.
	for i := range 3 {
		peer := Peer{Host: strconv.FormatInt(int64(i), 10)}
		for range 3 {
			iter.Remove(peer)
		}
	}
	require.Empty(t, iter.peers)

	// Open and close should be
	for range 3 {
		iter.Open()
		require.False(t, iter.closed)
	}
	for range 3 {
		iter.Close()
		require.True(t, iter.closed)
	}

	synctest.Test(t, func(t *testing.T) {
		iter := NewIterator()

		require.Equal(t, 0*time.Second, iter.TimeSinceUpdate())
		time.Sleep(1 * time.Second)
		require.Equal(t, 1*time.Second, iter.TimeSinceUpdate())

		iter.Close()
		time.Sleep(1 * time.Second)
		require.Equal(t, 2*time.Second, iter.TimeSinceUpdate())

		iter.Open()
		require.Equal(t, 0*time.Second, iter.TimeSinceUpdate())
		time.Sleep(5 * time.Second)
		require.Equal(t, 5*time.Second, iter.TimeSinceUpdate())
	})
}
