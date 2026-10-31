// Package web holds the dashboard: a small single-page app of plain HTML, CSS
// and JavaScript with no build step. It is compiled into the server binary, so
// running the API is all it takes to have the dashboard, and it is served from
// the same origin as the API (which is why it needs no CORS).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the dashboard's files, rooted so that index.html is at "/".
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		// The directory is embedded at compile time, so it cannot be missing.
		panic(err)
	}
	return sub
}
