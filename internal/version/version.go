package version

//go:generate go run ../../scripts/genversion

// Version is the default build version; the build pipeline injects the real
// value via -ldflags (see Makefile / CI). The default is generated from
// package.json by scripts/genversion (go generate ./internal/version), so
// the version lives in a single place.
var Version = "0.1.6"

// Commit is the git commit the binary was built from (optional, ldflags).
var Commit = "unknown"
