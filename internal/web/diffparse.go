package web

import (
	"html/template"
	"regexp"
	"strconv"
	"strings"

	"github.com/williamokano/kairos/internal/cli"
)

// diffLine is one line of a unified diff hunk, already syntax-highlighted
// and tagged with an anchor id for ?file=&line= deep links.
type diffLine struct {
	Kind         string // "context" | "add" | "del"
	OldNo, NewNo int    // 0 when not applicable (an added line has no OldNo, a removed line has no NewNo)
	HTML         template.HTML
	Anchor       string
}

type diffHunk struct {
	Header string
	Lines  []diffLine
}

// sideRow is one row of the side-by-side view — a context line occupies
// both columns identically; a run of removals paired against a run of
// additions (see buildSideRows) may leave either column blank.
type sideRow struct {
	OldNo, NewNo         int
	OldHTML, NewHTML     template.HTML
	OldKind, NewKind     string // "", "context", "add", "del"
	OldAnchor, NewAnchor string
}

// diffFileView is one changed file, ready for the template: the engine's
// own numstat summary (path, counts, binary) plus this file's hunks
// parsed out of the raw patch and highlighted, in both render modes.
type diffFileView struct {
	Index          int
	Path           string
	Added, Removed int
	Binary         bool
	InScope        bool
	Hunks          []diffHunk
	SideRows       [][]sideRow // one []sideRow per hunk, same length/order as Hunks
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// rawPatchFile is one "diff --git" section of a raw multi-file unified
// diff, before it's matched up with the engine's numstat-derived
// DiffFile summary.
type rawPatchFile struct {
	path  string
	hunks []diffHunk
}

// parsePatch splits a raw multi-file `git diff --unified=3` patch into
// per-file hunks. It only reads the +++ /--- headers and @@ hunk markers
// it needs; "index ..." lines, mode-change lines, and "Binary files
// differ" are skipped (a binary file's DiffFile.Binary already comes
// from numstat, this parser never needs to notice it).
func parsePatch(patch string) []rawPatchFile {
	var files []rawPatchFile
	var cur *rawPatchFile
	var curHunk *diffHunk
	oldNo, newNo := 0, 0
	fileIdx := 0

	flush := func() {
		if cur != nil {
			files = append(files, *cur)
		}
	}

	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &rawPatchFile{}
			curHunk = nil
			fileIdx = len(files)
		case strings.HasPrefix(line, "+++ b/"):
			if cur != nil {
				cur.path = strings.TrimPrefix(line, "+++ b/")
			}
		case strings.HasPrefix(line, "+++ /dev/null"):
			// deleted file: path already set from "--- a/..." below
		case strings.HasPrefix(line, "--- a/"):
			if cur != nil && cur.path == "" {
				cur.path = strings.TrimPrefix(line, "--- a/")
			}
		case hunkHeaderRe.MatchString(line):
			if cur == nil {
				continue
			}
			m := hunkHeaderRe.FindStringSubmatch(line)
			oldNo, _ = strconv.Atoi(m[1])
			newNo, _ = strconv.Atoi(m[2])
			cur.hunks = append(cur.hunks, diffHunk{Header: line})
			curHunk = &cur.hunks[len(cur.hunks)-1]
		case curHunk != nil && len(line) > 0 && (line[0] == '+' || line[0] == '-' || line[0] == ' '):
			dl := diffLine{}
			content := line[1:]
			switch line[0] {
			case ' ':
				dl.Kind, dl.OldNo, dl.NewNo = "context", oldNo, newNo
				oldNo++
				newNo++
			case '+':
				dl.Kind, dl.NewNo = "add", newNo
				newNo++
			case '-':
				dl.Kind, dl.OldNo = "del", oldNo
				oldNo++
			}
			dl.Anchor = "f" + strconv.Itoa(fileIdx) + "-p" + strconv.Itoa(len(curHunk.Lines))
			dl.HTML = highlightLine(cur.path, content)
			curHunk.Lines = append(curHunk.Lines, dl)
		}
		// Any other line ("\ No newline at end of file", "index ...",
		// mode-change lines) matches none of the cases above and is
		// silently skipped — none of it is a content line this viewer
		// renders.
	}
	flush()
	return files
}

// buildSideRows turns one hunk's linear +/-/context sequence into
// side-by-side rows: a context line occupies both columns; a run of
// consecutive removals is paired, position by position, against the run
// of consecutive additions that follows it — the same "zip adjacent
// blocks" heuristic most split-diff viewers use, not a full LCS
// realignment (a change that reorders lines within a block can pair a
// removal with an unrelated addition). Good enough for the common case
// this diff viewer targets: an agent's edit to a handful of nearby lines.
func buildSideRows(lines []diffLine) []sideRow {
	var rows []sideRow
	i := 0
	for i < len(lines) {
		if lines[i].Kind == "context" {
			l := lines[i]
			rows = append(rows, sideRow{
				OldNo: l.OldNo, NewNo: l.NewNo,
				OldHTML: l.HTML, NewHTML: l.HTML,
				OldKind: "context", NewKind: "context",
				OldAnchor: l.Anchor, NewAnchor: l.Anchor,
			})
			i++
			continue
		}
		var dels, adds []diffLine
		for i < len(lines) && lines[i].Kind == "del" {
			dels = append(dels, lines[i])
			i++
		}
		for i < len(lines) && lines[i].Kind == "add" {
			adds = append(adds, lines[i])
			i++
		}
		n := len(dels)
		if len(adds) > n {
			n = len(adds)
		}
		for j := 0; j < n; j++ {
			var row sideRow
			if j < len(dels) {
				row.OldNo, row.OldHTML, row.OldKind, row.OldAnchor = dels[j].OldNo, dels[j].HTML, "del", dels[j].Anchor
			}
			if j < len(adds) {
				row.NewNo, row.NewHTML, row.NewKind, row.NewAnchor = adds[j].NewNo, adds[j].HTML, "add", adds[j].Anchor
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// buildDiffFileViews merges the engine's numstat-derived file list (path,
// added/removed counts, binary) with parsePatch's hunks, matched by path
// — the two are computed by separate git invocations (DiffNumstat,
// DiffPatch) deliberately (see internal/workspace/diff.go), so this is
// where they're joined back together. scopeViolations marks a file
// InScope=false for the banner.
func buildDiffFileViews(files []cli.DiffFile, patch string, scopeViolations []string) []diffFileView {
	violated := make(map[string]bool, len(scopeViolations))
	for _, p := range scopeViolations {
		violated[p] = true
	}
	rawByPath := map[string]rawPatchFile{}
	for _, rf := range parsePatch(patch) {
		rawByPath[rf.path] = rf
	}

	out := make([]diffFileView, 0, len(files))
	for i, f := range files {
		v := diffFileView{
			Index: i, Path: f.Path, Added: f.Added, Removed: f.Removed,
			Binary: f.Binary, InScope: !violated[f.Path],
		}
		if rf, ok := rawByPath[f.Path]; ok {
			v.Hunks = rf.hunks
			for _, h := range rf.hunks {
				v.SideRows = append(v.SideRows, buildSideRows(h.Lines))
			}
		}
		out = append(out, v)
	}
	return out
}
