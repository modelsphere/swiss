// Package web carries the built SPA.
//
// Embedding it means the UI is versioned with the API that serves it: one
// binary, one artifact, and no way for a cluster to run a frontend that is
// newer than the server behind it -- which matters when there is one swissd per
// cluster and they drift.
//
// `dist` is committed empty (a .gitkeep) so that `go build` works without node
// installed. A build with no UI in it serves an explanatory page rather than a
// blank one; run `npm run build` in web/, or start swissd with -web-dir to
// serve from disk.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built site, or nil when nothing was built into this binary.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
