package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePullMessage(t *testing.T) {
	s := "Successfully pulled image \"docker.io/library/nginx:mainline-alpine\" in 873.420598ms (873.428863ms including waiting)"
	d, err := parsePullMessage(s)
	require.NoError(t, err)
	require.Equal(t, 873428863*time.Nanosecond, d)
}

func TestCreatePlot(t *testing.T) {
	results := []dsResult{}
	for i := 1; i <= 10; i++ {
		d, err := time.ParseDuration(fmt.Sprintf("%ds", i))
		require.NoError(t, err)
		results = append(results, dsResult{start: time.Now().Add(d), duration: d})
	}
	err := createPlot(results)
	require.NoError(t, err)
}
