// Package version exposes the depgraph build version. The release workflow
// and Makefile inject the actual value via -ldflags
// `-X github.com/sundaycrafts/depgraph/internal/version.Version=<tag>`.
package version

// Version is the depgraph build version, set at link time. Defaults to "dev"
// for unreleased builds (e.g. `go build` without -ldflags).
var Version = "dev"
