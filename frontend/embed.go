// Package frontend embeds the Vite build output (dist/) into the binary.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
