package tui

import (
	"fmt"
	"strings"
)

// viewRunners is honest about scope: 07-runners.md's remote-runner
// management (add/probe/drain) does not exist yet — there is exactly one
// runner, "local", and this renders that one real row plus the daemon's
// real doctor checks as its toolchain column, rather than inventing
// per-runner data no endpoint provides. "A screen that shows one row is
// honest about there being one row" — 09-cli-and-tui.md, verbatim.
func (m Model) viewRunners() string {
	var b strings.Builder
	b.WriteString("RUNNERS\n\n")
	b.WriteString("NAME   KIND   TOOLCHAIN\n")
	toolchain := "—"
	if m.runners.doctorErr == nil && len(m.runners.doctor.Checks) > 0 {
		var parts []string
		for _, c := range m.runners.doctor.Checks {
			mark := "✓"
			if !c.OK {
				mark = "✗"
			}
			parts = append(parts, fmt.Sprintf("%s%s", mark, c.Name))
		}
		toolchain = strings.Join(parts, " · ")
	}
	fmt.Fprintf(&b, "local  local  %s\n", toolchain)
	if m.runners.doctorErr != nil {
		fmt.Fprintf(&b, "\ndoctor check failed: %v\n", m.runners.doctorErr)
	}
	b.WriteString("\nNAV  esc\n")
	return b.String()
}
