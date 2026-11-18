// Package version carries the server build version. It is reported through
// GET /api/v1/version and logged at boot, and can be overridden at build time with
// -ldflags "-X github.com/cmspeedau/gamedepot/internal/version.Version=0.2.0".
package version

// Version is the server release version. Must move with tagged releases; the
// dashboard and clients compare against it during upgrades.
var Version = "0.1.0"
