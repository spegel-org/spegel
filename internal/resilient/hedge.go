package resilient

import (
	"context"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// Hedger keeps track of durations and triggers hedges after the given quantile duration.
type Hedger struct {
	hist        *hdrhistogram.Histogram
	percentiles []float64
	initial     time.Duration
	mx          sync.RWMutex
}

// NewHedger returns a hedger with the given quantile.
func NewHedger(percentiles []float64, initial time.Duration) *Hedger {
	hist := hdrhistogram.New(0, int64(500*time.Millisecond), 1)
	return &Hedger{
		percentiles: percentiles,
		hist:        hist,
		initial:     initial,
	}
}

// Size returns the amount of times a hedge channel will be triggered.
func (h *Hedger) Size() int {
	return len(h.percentiles)
}

// Observe adds the duration to be used in hedge duration calculation.
func (h *Hedger) Observe(d time.Duration) error {
	h.mx.Lock()
	defer h.mx.Unlock()
	return h.hist.RecordValue(d.Milliseconds())
}

// Channel returns a channel which tirggers after the percentile hedge durations.
func (h *Hedger) Channel(ctx context.Context) <-chan any {
	ch := make(chan any, len(h.percentiles))
	go func() {
		start := time.Now()
		for _, percentile := range h.percentiles {
			h.mx.RLock()
			hedgeDuration := time.Duration(h.hist.ValueAtPercentile(percentile)) * time.Millisecond
			h.mx.RUnlock()
			if hedgeDuration == 0 {
				hedgeDuration = h.initial
			}

			d := max(hedgeDuration-time.Since(start), 0)
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}

			ch <- nil
		}
	}()
	return ch
}
