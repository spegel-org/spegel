package channel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMerge(t *testing.T) {
	t.Parallel()

	ch1 := make(chan int)
	ch2 := make(chan int)
	ch3 := make(chan int)
	go func() {
		defer close(ch1)
		ch1 <- 1
		ch1 <- 2
	}()
	go func() {
		defer close(ch2)
		ch2 <- 3
	}()
	go func() {
		defer close(ch3)
		ch3 <- 4
		ch3 <- 5
		ch3 <- 6
	}()
	outCh := Merge(ch1, ch2, ch3)

	results := []int{}
	waitCtx, waitCancel := context.WithCancel(t.Context())
	go func() {
		defer waitCancel()
		for v := range outCh {
			results = append(results, v)
		}
	}()
	<-waitCtx.Done()

	expected := []int{1, 2, 3, 4, 5, 6}
	require.ElementsMatch(t, expected, results)
}
