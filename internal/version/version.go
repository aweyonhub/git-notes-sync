package version

// Version is injected at build time via -ldflags (see Makefile / CI).
var Version = "0.1.0"

// Commit is the git commit the binary was built from (optional, ldflags).
var Commit = "unknown"
