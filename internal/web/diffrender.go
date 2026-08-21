// Server-side syntax highlighting for the diff viewer — AGENTS.md's
// approved-dependency table: "github.com/alecthomas/chroma/v2 —
// server-side only, for the web diff viewer. Rendering to spans in Go
// means no client-side highlighter, no 400 KB of JS, and no CSP
// exception." This file is that rendering; nothing here ever ships to
// the browser as script.
package web

import (
	"bytes"
	"html/template"
	"net/http"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// chromaStyleName picks a dark theme matching app.css's own dark-first
// palette (:root's default, --bg: #0d1117 — the same background
// github-dark uses). It does not repaint under prefers-color-scheme:
// light the way app.css's own tokens do; a fixed highlight palette that
// only really suits one of the two themes is a real, known simplification
// (see L23-webui-revamp.md), not an oversight.
const chromaStyleName = "github-dark"

func chromaStyle() *chroma.Style {
	if s := styles.Get(chromaStyleName); s != nil {
		return s
	}
	return styles.Fallback
}

var (
	chromaCSSOnce sync.Once
	chromaCSSText string
)

// chromaStylesheet renders the current style's CSS once (chroma's own
// Formatter.WriteCSS) and caches it — served at GET /static/chroma.css
// rather than hand-vendored, so it can never drift out of sync with the
// pinned chroma version's actual class names.
func chromaStylesheet() string {
	chromaCSSOnce.Do(func() {
		f := chromahtml.New(chromahtml.WithClasses(true))
		var buf bytes.Buffer
		_ = f.WriteCSS(&buf, chromaStyle())
		chromaCSSText = buf.String()
	})
	return chromaCSSText
}

func handleChromaCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(chromaStylesheet()))
}

// highlightLine tokenises one diff line's content (the raw source text,
// with the leading +/-/space diff marker already stripped) and renders it
// to a bare run of `<span class="chroma-...">` — no surrounding
// <pre>/<code>, via PreventSurroundingPre, so it drops straight into this
// package's own per-line markup. filename picks the lexer by extension
// (lexers.Match); an unrecognised extension falls back to plain text
// rather than guessing.
func highlightLine(filename, content string) template.HTML {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(content)) //nolint:gosec // escaped below
	}
	f := chromahtml.New(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true))
	var buf bytes.Buffer
	if err := f.Format(&buf, chromaStyle(), iterator); err != nil {
		return template.HTML(template.HTMLEscapeString(content)) //nolint:gosec // escaped below
	}
	return template.HTML(buf.String()) //nolint:gosec // chroma's own HTML-escaped span markup, not raw user input
}
