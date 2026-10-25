// Package web hosts the compiled SPA embedded into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded SPA filesystem rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// Available reports whether a build is present (dist/index.html exists).
func Available() bool {
	_, err := fs.Stat(distFS, "dist/index.html")
	return err == nil
}
