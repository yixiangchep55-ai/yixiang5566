package uiembed

import (
	"embed"
	"io/fs"
)

// distFS embeds the built explorer frontend so the local API can serve it
// directly from the binary.
//
//go:embed dist
var distFS embed.FS

func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
