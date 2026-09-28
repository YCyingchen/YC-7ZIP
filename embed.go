package main

import "embed"

// webFS holds the user interface. index.html sits at the root of the embedded
// tree, which is also where the file lives in the repository, so the same HTML
// can be opened directly from disk during development.
//
//go:embed index.html assets
var webFS embed.FS
