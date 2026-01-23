package version

import (
	"errors"
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
	GoVersion string `json:"goVersion" text:"Go Version"`
	Version   string `json:"version" text:"Version"`
	Commit    string `json:"commit" text:"Commit"`
}

type Runtime struct {
	Distro string `json:"distro" text:"Distribution"`
	OS     string `json:"os" text:"OS"`
	Arch   string `json:"arch" text:"Architecture"`
}

func (i Info) String() string {
	return ""
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
