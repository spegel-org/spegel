package version

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

type Info struct {
	Build   Build   `json:"build" text:"Build"`
	Runtime Runtime `json:"runtime" text:"Runtime"`
}

type Build struct {
	GoVersion string `json:"goVersion"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
}

type Runtime struct {
	Distro string `json:"distro"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
}

func (i Info) Preflight() error {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return nil
	}
	if i.Runtime.OS != "linux" && i.Runtime.Distro != "Distroless" {
		return fmt.Errorf("unsupported container distro %s", i.Runtime.Distro)
	}
	return nil
}

func Load() (Info, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Info{}, errors.New("could not read build info")
	}
	info := Info{
		Build: Build{
			GoVersion: bi.GoVersion,
		},
		Runtime: Runtime{
			Distro: getDistro(),
		},
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "GOARCH":
			info.Runtime.Arch = s.Value
		case "GOOS":
			info.Runtime.OS = s.Value
		case "vcs.revision":
			info.Build.Commit = s.Value
		case "vcs.modified":
			modified, err := strconv.ParseBool(s.Value)
			if err != nil {
				return Info{}, err
			}
			info.Build.Version = getVersion(bi.Main.Version, modified)
		}
	}
	return info, nil
}

func getDistro() string {
	unknownDistro := "unknown"
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return unknownDistro
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(line[len("PRETTY_NAME="):], `"`)
		}
	}
	return unknownDistro
}

func getVersion(mainVersion string, modified bool) string {
	develVersion := "devel"
	if modified {
		return develVersion
	}
	if mainVersion == "" {
		return develVersion
	}
	return mainVersion
}
