package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleProjectsKey drives both the plain project list and the
// create-project flow (name, then the directory picker — reusing
// cli.Client.BrowseFS, the SAME GET /fs/browse endpoint the web UI's
// picker calls, never a parallel filesystem walk). 'n' starts creating;
// esc at any point in that flow cancels back to the plain list, matching
// this codebase's "esc always, unconditionally, returns" rule (model.go).
func (m Model) handleProjectsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.projects.creating {
		return m.handleProjectCreateKey(msg)
	}
	switch msg.String() {
	case "j":
		if m.projects.cursor < len(m.projects.projects)-1 {
			m.projects.cursor++
		}
	case "k":
		if m.projects.cursor > 0 {
			m.projects.cursor--
		}
	case "gg":
		m.projects.cursor = 0
	case "G":
		m.projects.cursor = len(m.projects.projects) - 1
	case "n":
		m.projects.creating = true
		m.projects.name = ""
		m.projects.picking = false
		m.projects.createErr = nil
		m.mode = ModeINPUT
	}
	return m, nil
}

func (m Model) handleProjectCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.projects.picking {
		// Step 1: a plain name field — the picker takes over for path,
		// there is no free-text path field at all (the user's own ask:
		// "a rich selector... not always I remember the path from the
		// top of my head").
		switch msg.String() {
		case "esc":
			m.projects.creating = false
			m.mode = ModeNAV
			return m, nil
		case "enter":
			if m.projects.name == "" {
				return m, nil
			}
			m.projects.picking = true
			m.mode = ModeNAV
			return m, m.browseFS("")
		case "backspace":
			m.projects.name = trimLastRune(m.projects.name)
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.projects.name += string(msg.Runes)
			}
			return m, nil
		}
	}

	// Step 2: the directory picker itself.
	entries := m.projects.picker.resp.Entries
	switch msg.String() {
	case "esc":
		m.projects.creating = false
		m.projects.picking = false
		m.mode = ModeNAV
		return m, nil
	case "j":
		if m.projects.picker.cursor < len(entries)-1 {
			m.projects.picker.cursor++
		}
	case "k":
		if m.projects.picker.cursor > 0 {
			m.projects.picker.cursor--
		}
	case "u", "backspace":
		if m.projects.picker.resp.Parent != "" {
			return m, m.browseFS(m.projects.picker.resp.Parent)
		}
	case "enter":
		if m.projects.picker.cursor >= 0 && m.projects.picker.cursor < len(entries) {
			return m, m.browseFS(entries[m.projects.picker.cursor].Path)
		}
	case "s":
		// Select the CURRENTLY browsed directory (not an entry inside
		// it) as the new Project's path — the picker's own "this one"
		// action, distinct from 'enter' which descends further.
		path := m.projects.picker.resp.Path
		if path == "" {
			return m, nil
		}
		return m, m.createProject(m.projects.name, path)
	}
	return m, nil
}

func (m Model) viewProjects() string {
	var b strings.Builder
	b.WriteString("PROJECTS\n\n")
	if m.projects.creating {
		return m.viewProjectCreate(&b)
	}
	if m.projects.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.projects.err)
	}
	if len(m.projects.projects) == 0 {
		b.WriteString("no projects yet — press n to create one\n")
	}
	for i, p := range m.projects.projects {
		cursor := " "
		if i == m.projects.cursor {
			cursor = "▸"
		}
		git := ""
		if p.GitBacked {
			git = " (git)"
		}
		fmt.Fprintf(&b, "%s %-12s %s%s\n", cursor, p.Name, p.RepoPath, git)
	}
	b.WriteString("\nNAV  j/k move  n new project  h home  esc\n")
	if m.statusLine != "" {
		fmt.Fprintf(&b, "\n%s\n", m.statusLine)
	}
	return b.String()
}

func (m Model) viewProjectCreate(b *strings.Builder) string {
	if !m.projects.picking {
		fmt.Fprintf(b, "new project — name:\n\n▏%s", m.projects.name)
		if m.mode == ModeINPUT {
			b.WriteString("█")
		}
		b.WriteString("\n\nINPUT  ⏎ next: pick a path  esc cancel\n")
		return b.String()
	}

	fmt.Fprintf(b, "new project %q — pick a directory:\n\n", m.projects.name)
	if m.projects.picker.err != nil {
		fmt.Fprintf(b, "error: %v\n", m.projects.picker.err)
	}
	fmt.Fprintf(b, "%s\n\n", m.projects.picker.resp.Path)
	for i, e := range m.projects.picker.resp.Entries {
		cursor := " "
		if i == m.projects.picker.cursor {
			cursor = "▸"
		}
		git := ""
		if e.IsGit {
			git = "  (git)"
		}
		fmt.Fprintf(b, "%s %s%s\n", cursor, e.Name, git)
	}
	if m.projects.createErr != nil {
		fmt.Fprintf(b, "\nerror creating project: %v\n", m.projects.createErr)
	}
	b.WriteString("\nNAV  j/k move  ⏎ open dir  u up  s select this dir  esc cancel\n")
	return b.String()
}
