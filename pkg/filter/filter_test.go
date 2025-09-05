package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		str      string
		patterns []string
		expected bool
	}{
		{
			name:     "empty patterns",
			str:      "test",
			patterns: []string{},
			expected: true,
		},
		{
			name:     "nil patterns",
			str:      "test",
			patterns: nil,
			expected: true,
		},
		{
			name:     "single pattern match",
			str:      "test123",
			patterns: []string{"test.*"},
			expected: true,
		},
		{
			name:     "single pattern no match",
			str:      "other123",
			patterns: []string{"test.*"},
			expected: false,
		},
		{
			name:     "multiple patterns match first",
			str:      "test123",
			patterns: []string{"test.*", "other.*"},
			expected: true,
		},
		{
			name:     "multiple patterns match second",
			str:      "other123",
			patterns: []string{"test.*", "other.*"},
			expected: true,
		},
		{
			name:     "multiple patterns no match",
			str:      "nomatch123",
			patterns: []string{"test.*", "other.*"},
			expected: false,
		},
		{
			name:     "exact match",
			str:      "test",
			patterns: []string{"^test$"},
			expected: true,
		},
		{
			name:     "exact no match",
			str:      "test123",
			patterns: []string{"^test$"},
			expected: false,
		},
		{
			name:     "empty pattern strings",
			str:      "test",
			patterns: []string{"", "test.*", "  "},
			expected: true,
		},
		{
			name:     "invalid regex pattern",
			str:      "test",
			patterns: []string{"[invalid"},
			expected: false,
		},
		{
			name:     "case sensitive match",
			str:      "Test123",
			patterns: []string{"test.*"},
			expected: false,
		},
		{
			name:     "case insensitive match",
			str:      "Test123",
			patterns: []string{"(?i)test.*"},
			expected: true,
		},
		{
			name:     "complex regex patterns",
			str:      "docker.io/library/nginx:latest",
			patterns: []string{"^docker\\.io/", "^gcr\\.io/"},
			expected: true,
		},
		{
			name:     "no match with complex patterns",
			str:      "quay.io/namespace/repo:latest",
			patterns: []string{"^docker\\.io/", "^gcr\\.io/"},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := FilterString(tt.str, tt.patterns)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Benchmark test to ensure performance is reasonable
func BenchmarkFilterString(b *testing.B) {
	patterns := []string{"test.*", "other.*"}
	str := "test123"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FilterString(str, patterns)
	}
}
