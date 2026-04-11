package resilient

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
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
			require.Equal(t, 3, hedger.Size())
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
					require.True(t, ok)
					durations = append(durations, time.Since(start))
				}
				require.Equal(t, tt.expected, durations)
			})
		})
	}
}
