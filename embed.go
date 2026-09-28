package main

import "embed"

// webFS holds the user interface. index.html sits at the root of the embedded
// tree, which is also where the file lives in the repository, so the same HTML
// can be opened directly from disk during development.
//
//go:embed index.html assets
var webFS embed.FS

// changelogMD is the release notes, embedded so the in-app settings panel can
// show what a version changed without reaching the network — a NAS usually
// sits on an isolated LAN, and "what does this upgrade contain" is exactly
// what someone wants to read before applying it.
//
//go:embed CHANGELOG.md
var changelogMD []byte
