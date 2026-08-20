package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// screenNames resolves a typed screen name to a Screen. Deliberately a
// small, exact map — 09-cli-and-tui.md: "no fuzzy-search over everything,
// no command history as a feature." A typo gets no match, not a guess.
var screenNames = map[string]Screen{
	"home":          ScreenHome,
	"conversation":  ScreenConversation,
	"conversations": ScreenConversation,
	"run":           ScreenRunInspector,
	"runs":          ScreenHome,
	"logs":          ScreenLogs,
	"inbox":         ScreenInbox,
	"runners":       ScreenRunners,
	"bench":         ScreenBenchmark,
	"benchmark":     ScreenBenchmark,
}

// paletteVerbs resolves a typed verb to the screen that best represents it
// in this document's scope — a real, if narrow, verb-to-screen mapping
// rather than a general command dispatcher (kairos run/approve/etc. as
// full actions from the palette are Future work).
var paletteVerbs = map[string]Screen{
	"approve": ScreenInbox,
	"inbox":   ScreenInbox,
	"status":  ScreenHome,
}

// resolvePalette implements the command palette's contract exactly:
// resolve any (bare, since this codebase's IDs carry no "run_"/"ht_"
// prefix — see L15-tui.md's Documented decisions) ULID to the run
// inspector, a screen name, or a verb. No fuzzy matching, no history.
// resolvePalette returns (screen, runID, isRunID, matched).
func (m Model) resolvePalette(input string) (Screen, string, bool, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, "", false, false
	}
	if s, ok := screenNames[input]; ok {
		return s, "", false, true
	}
	if s, ok := paletteVerbs[input]; ok {
		return s, "", false, true
	}
	if looksLikeULID(input) {
		return ScreenRunInspector, input, true, true
	}
	return 0, "", false, false
}

// looksLikeULID is a narrow shape check (26 alphanumeric characters,
// Crockford base32 alphabet) — not a full ULID validator, just enough to
// decide "try this as a run id" versus "this typo resolves to nothing."
func looksLikeULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

func (m Model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.paletteOpen = false
		m.paletteInput = ""
		return m, nil
	case "enter":
		screen, runID, isRun, matched := m.resolvePalette(m.paletteInput)
		m.paletteOpen = false
		m.paletteInput = ""
		if !matched {
			m.statusLine = "no match"
			return m, nil
		}
		if isRun {
			m.navigate(ScreenRunInspector)
			m.runInspector.runID = runID
			return m, m.fetchRunState(runID)
		}
		m.navigate(screen)
		return m, m.refreshCmd()
	case "backspace":
		m.paletteInput = trimLastRune(m.paletteInput)
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.paletteInput += string(msg.Runes)
		}
		return m, nil
	}
}
