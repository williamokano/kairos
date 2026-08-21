package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"
)

// funcMap is 10-webui.md's render.go template funcs: dur, cost, id, sev,
// relTime. The diff viewer's own per-line rendering (highlightLine,
// diffrender.go) runs ahead of template execution in Go, not through
// this map — it returns pre-built template.HTML, not a string a template
// func would format.
var funcMap = template.FuncMap{
	"dur": func(d time.Duration) string {
		if d < time.Second {
			return "0s"
		}
		return d.Round(time.Second).String()
	},
	"cost": func(usd float64) string { return fmt.Sprintf("$%.2f", usd) },
	"id": func(s string) string {
		if len(s) <= 12 {
			return s
		}
		return s[:12]
	},
	"sev": func(s string) string {
		switch s {
		case "critical", "high":
			return "⚠"
		case "medium", "low":
			return "•"
		default:
			return "·"
		}
	},
	"relTime": func(v any) string {
		var t time.Time
		switch x := v.(type) {
		case time.Time:
			t = x
		case string:
			parsed, err := time.Parse(time.RFC3339, x)
			if err != nil {
				return x
			}
			t = parsed
		default:
			return "—"
		}
		if t.IsZero() {
			return "—"
		}
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		}
	},
}

var (
	cachedTemplates *template.Template
	cachedOnce      sync.Once
)

// templates returns the parsed template set — parsed once and cached in
// the release build, re-parsed on every call in dev mode (embed_dev.go's
// devMode=true), matching ADR 0007's "dev mode swaps the filesystem, not
// the code" exactly.
func templates() (*template.Template, error) {
	if !devMode {
		var err error
		cachedOnce.Do(func() {
			cachedTemplates, err = parseAll()
		})
		return cachedTemplates, err
	}
	return parseAll()
}

func parseAll() (*template.Template, error) {
	return template.New("root").Funcs(funcMap).ParseFS(templatesFS(),
		"*.gohtml", "frag/*.gohtml")
}

// renderPage renders name's content template, then wraps it in "layout" —
// 10-webui.md's "Page" route kind: a full, bookmarkable document with its
// fragments rendered inline on first paint, complete with JavaScript
// disabled.
//
// Content is composed into layout by rendering the content template to a
// buffer first (rather than Go template "block" overriding, which shares
// one global namespace across every file matched by the same ParseGlob and
// so cannot hold N independent per-page bodies at once) — a deliberate,
// documented choice over the block-inheritance idiom, not an oversight.
// renderPage's optional trailing runID is the current page's run context,
// if any — rendered onto <body data-run-id> so app.js's keyboard model can
// answer "is there an active run here" (the same conditional 'l'/'c'
// bindings the TUI's own handleRunInspectorKey/global 'l' binding apply:
// see keys.go's `if m.runInspector.runID != ""`) without the DOM-scraping
// or per-page JS wiring a less general mechanism would need. Omitted
// entirely (zero-value "") on pages with no single associated run (home,
// runs list, doctor, findings, and so on).
func renderPage(w http.ResponseWriter, title, name string, data any, runID ...string) {
	t, err := templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var body bytes.Buffer
	if err := t.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var rid string
	if len(runID) > 0 {
		rid = runID[0]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", struct {
		Title string
		Body  template.HTML
		RunID string
	}{Title: title, Body: template.HTML(body.String()), RunID: rid}); err != nil { //nolint:gosec // body is our own already-escaped template output, not raw user input
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderFragment renders name as a bare fragment — 10-webui.md's
// "Fragment" route kind, always under /frag/ and never a full document.
func renderFragment(w http.ResponseWriter, name string, data any) {
	t, err := templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}
