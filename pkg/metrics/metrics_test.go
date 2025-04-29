package metrics

import (
	"testing"
)

func TestRegister(t *testing.T) {
	t.Parallel()

	Register()
}
