//go:build dev

package web

import (
	"io/fs"
	"os"
)

const devMode = true

// Under -tags dev, templates and static assets are read live from disk
// instead of the embedded FS — 10-webui.md: "-tags dev binds the template
// FS to os.DirFS(\"internal/web\") and re-parses per request." This
// assumes the binary runs with the repo root as its working directory
// during development (go run ./cmd/kairos from the repo root), exactly as
// the doc's own example does — a real constraint of this exact mechanism,
// not an oversight.
func templatesFS() fs.FS { return os.DirFS("internal/web/templates") }
func staticFS() fs.FS    { return os.DirFS("internal/web/static") }
