// Package version carries the build version shown in the TUIs.
//
// The default matches the latest release tag; release builds override it
// at link time:
//
//	go build -ldflags "-X prp-gns3/internal/version.Version=v0.5.3" ./cmd/prpd
package version

// Version is the release version (e.g. "v0.5.3" or "dev").
// Overridden by CI/Docker via -ldflags "-X .../version.Version=<tag>".
var Version = "v0.5.3"
