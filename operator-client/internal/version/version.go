// Package version is the adbo client build version.
// Override it at build time:
//
//	go build -ldflags "-X github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version.Version=$(git describe --tags --always)"
package version

// Version is the adbo client version. "dev" means the binary was not stamped.
var Version = "dev"
