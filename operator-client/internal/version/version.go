// Package version reports which adbo build is running.
//
// Release builds stamp the version at link time:
//
//	go build -ldflags "-X github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version.Version=v0.1.0" ./cmd/adbo
package version

import (
	"runtime"
	"runtime/debug"
)

// Version is set with -ldflags -X. Empty means a local build.
var Version = ""

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"gitCommit,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get prefers the -ldflags version, then the module version recorded by
// `go install ...@vX`, then "dev". The commit comes from Go's VCS stamping.
func Get() Info {
	info := Info{
		Version:   Version,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if info.Version == "" {
			info.Version = "dev"
		}
		return info
	}
	if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.GitCommit = s.Value
			if len(info.GitCommit) > 12 {
				info.GitCommit = info.GitCommit[:12]
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && info.GitCommit != "" {
		info.GitCommit += "-dirty"
	}
	return info
}
