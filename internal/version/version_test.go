package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreflight(t *testing.T) {
	versionInfo := Info{
		Runtime: Runtime{
			OS:     "linux",
			Distro: "ubuntu",
		},
	}
	err := versionInfo.Preflight()
	require.NoError(t, err)

	t.Setenv("KUBERNETES_SERVICE_HOST", "foo")
	err = versionInfo.Preflight()
	require.EqualError(t, err, "unsupported container distro ubuntu")

	versionInfo.Runtime.Distro = "Distroless"
	err = versionInfo.Preflight()
	require.NoError(t, err)
}
