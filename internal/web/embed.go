//go:build !dev

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:templates static
var assets embed.FS

const devMode = false

// templatesFS/staticFS are swapped for os.DirFS under -tags dev (see
// embed_dev.go) — ADR 0007's "dev mode swaps the filesystem, not the
// code": editing a template never requires a rebuild in dev mode, and the
// release build parses the embedded FS once.
func templatesFS() fs.FS {
	sub, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err) // embed.FS with a missing "templates" dir is a build-time bug, not a runtime one
	}
	return sub
}

func staticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
