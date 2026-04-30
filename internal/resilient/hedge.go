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

// durationAtPercentile returns the duration at the given percentile, or the the initial duration if no data is available.
func (h *Hedger) durationAtPercentile(percentile float64) time.Duration {
	h.mx.RLock()
	value := h.hist.ValueAtPercentile(percentile)
	h.mx.RUnlock()
	if value == 0 {
		return h.initial
	}
	return time.Duration(value) * time.Millisecond
}

// HighestPercentileDuration returns the duration for the highest percentile.
func (h *Hedger) HighestPercentileDuration() time.Duration {
	return h.durationAtPercentile(h.percentiles[len(h.percentiles)-1])
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
			hedgeDuration := h.durationAtPercentile(percentile)

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
