package preflight

import (
	"fmt"
	"os"

	"github.com/kvick-org/pkg/version"
)

const (
	KubernetesEnvVar = "KUBERNETES_SERVICE_HOST"
)

func Check(versionInfo version.Info) error {
	if os.Getenv(KubernetesEnvVar) == "" {
		return nil
	}
	if versionInfo.Runtime.OS == "linux" && versionInfo.Runtime.Distro == "Distroless" {
		return nil
	}
	return fmt.Errorf("unsupported container distro %s", versionInfo.Runtime.Distro)
}
