package httpx

import (
	"context"
	"errors"
	"net/http/httptrace"
	"net/netip"
	"sync"
	"time"
)

type happyEyeballsResult[T any] struct {
	val T
	err error
}

type HappyEyeballsCallback[T any] func(context.Context, netip.Addr) (T, error)

func HappyEyeballs[T any](ctx context.Context, ipAddrs []netip.Addr, cb HappyEyeballsCallback[T]) (T, error) {
	if len(ipAddrs) == 0 {
		var zeroT T
		return zeroT, errors.New("empty list of address ports")
	}
	if len(ipAddrs) == 1 {
		return cb(ctx, ipAddrs[0])
	}

	// Sort addresses with priotiry for IP6.
	ipAddrs = sortIPAddrs(ipAddrs)

	// Cancelable context per race.
	var ctxMx sync.Mutex
	raceCtxs := []context.Context{}
	raceCancels := []context.CancelFunc{}
	for range ipAddrs {
		raceCtx, raceCancel := context.WithCancel(ctx)
		raceCtxs = append(raceCtxs, raceCtx)
		raceCancels = append(raceCancels, raceCancel)
	}

	// Race every address.
	resultCh := make(chan happyEyeballsResult[T], len(ipAddrs))
	for i, addrPort := range ipAddrs {
		// Delay in between dials.
		if i > 0 {
			select {
			case <-ctx.Done():
				var zeroT T
				return zeroT, ctx.Err()
			// TODO: Add jitter to request firing.
			case <-time.After(15 * time.Millisecond):
			}
		}
		go func() {
			trace := &httptrace.ClientTrace{
				GotConn: func(httptrace.GotConnInfo) {
					ctxMx.Lock()
					defer ctxMx.Unlock()

					for j, cancel := range raceCancels {
						if j == i {
							continue
						}
						cancel()
					}
				},
			}
			cbCtx := httptrace.WithClientTrace(raceCtxs[i], trace)
			val, err := cb(cbCtx, addrPort)
			if err != nil {
				resultCh <- happyEyeballsResult[T]{
					err: err,
				}
				return
			}
			resultCh <- happyEyeballsResult[T]{
				val: val,
			}
		}()
	}

	// Wait for first result.
	errs := []error{}
	for {
		select {
		case <-ctx.Done():
			var zeroT T
			return zeroT, ctx.Err()
		case result := <-resultCh:
			if result.err != nil {
				errs = append(errs, result.err)
				// Return error when all failed.
				if len(errs) == len(ipAddrs) {
					var zeroT T
					return zeroT, errors.Join(errs...)
				}
				continue
			}
			return result.val, nil
		}
	}
}

func sortIPAddrs(ipAddrs []netip.Addr) []netip.Addr {
	primary := []netip.Addr{}
	secondary := []netip.Addr{}
	for _, ipAddr := range ipAddrs {
		if ipAddr.Is4() {
			secondary = append(secondary, ipAddr)
		} else {
			primary = append(primary, ipAddr)
		}
	}
	return append(primary, secondary...)
}
