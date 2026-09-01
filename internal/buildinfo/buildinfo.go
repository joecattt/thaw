// Package buildinfo holds the single source of truth for the build version,
// injected at release time via -ldflags "-X .../internal/buildinfo.Version=...".
// Every package that needs to display a version reads Version from here so the
// binary, the recap HTML, and the briefing all agree.
package buildinfo

// Version is the release version, overridden by the linker at build time.
// Defaults to "dev" for `go build`/`go run` (source builds).
var Version = "dev"
