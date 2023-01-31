package state

import (
	"sync"
	"time"
)

func merge(cs ...<-chan time.Time) <-chan time.Time {
	var wg sync.WaitGroup
	out := make(chan time.Time)

	output := func(c <-chan time.Time) {
		for n := range c {
			out <- n
		}
		wg.Done()
	}
	wg.Add(len(cs))
	for _, c := range cs {
		go output(c)
	}

	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
