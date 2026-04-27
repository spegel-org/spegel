package resilient

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-openapi/testify/v2/require"
)

func TestHedger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		observations []time.Duration
		expected     []time.Duration
	}{
		{
			name: "no observed values",
			expected: []time.Duration{
				100 * time.Millisecond,
				100 * time.Millisecond,
				100 * time.Millisecond,
			},
		},
		{
			name: "with observed values",
			observations: []time.Duration{
				50 * time.Millisecond,
				100 * time.Millisecond,
				150 * time.Millisecond,
				200 * time.Millisecond,
			},
			expected: []time.Duration{
				151 * time.Millisecond,
				207 * time.Millisecond,
				207 * time.Millisecond,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hedger := NewHedger([]float64{80, 90, 95}, 100*time.Millisecond)
			require.EqualT(t, 3, hedger.Size())
			for _, d := range tt.observations {
				err := hedger.Observe(d)
				require.NoError(t, err)
			}

			synctest.Test(t, func(t *testing.T) {
				ch := hedger.Channel(t.Context())
				start := time.Now()
				durations := []time.Duration{}
				for range 3 {
					_, ok := <-ch
					require.TrueT(t, ok)
					durations = append(durations, time.Since(start))
				}
				require.SliceEqualT(t, tt.expected, durations)
			})
		})
	}
}

func TestHedgerHighestPercentileDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		percentiles  []float64
		observations []time.Duration
		want         time.Duration
	}{
		{
			name:         "no observed values",
			percentiles:  []float64{80, 90, 95},
			observations: []time.Duration{},
			want:         100 * time.Millisecond,
		},
		{
			name:         "no percentiles",
			percentiles:  []float64{},
			observations: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond},
			want:         100 * time.Millisecond,
		},
		{
			name:         "with percentiles and observed values",
			percentiles:  []float64{80, 90, 95},
			observations: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond},
			want:         150 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hedger := NewHedger(tt.percentiles, 100*time.Millisecond)
			for _, d := range tt.observations {
				err := hedger.Observe(d)
				require.NoError(t, err)
			}
			require.InEpsilon(t, tt.want, hedger.HighestPercentileDuration(), 0.01)
		})
	}
}
