package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cronSourceFieldCount is the number of steps handleSourceCreateKey walks
// through: id, flow, schedule, weekday, hour, minute.
const cronSourceFieldCount = 6

// handleSourceCreateKey is ScreenSourceCreate's real, structured-field
// form — 08-triggers.md's own named Future work ("--config takes raw
// JSON... a friendlier per-kind flag surface is cosmetic, deferred"),
// closed here for "cron", the one source kind that's a real,
// constructible Source today (github/jira/linear/plugin are never
// registered anywhere in this tree — see internal/tasksource.
// BuildCronConfig's doc comment). Fields are posted as discrete values;
// the daemon builds the real config string server-side, so this screen
// never needs internal/tasksource itself (ADR 0008).
func (m Model) handleSourceCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.cronSource.creating {
		m.cronSource = cronSourceState{creating: true, schedule: "daily"}
		m.mode = ModeINPUT
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.cronSource.creating = false
		m.mode = ModeNAV
		return m, nil
	case "enter":
		if m.cronSource.field < cronSourceFieldCount-1 {
			if m.cronSource.field == 0 && m.cronSource.id == "" {
				return m, nil
			}
			if m.cronSource.field == 1 && m.cronSource.flow == "" {
				return m, nil
			}
			m.cronSource.field++
			return m, nil
		}
		weekday, _ := strconv.Atoi(m.cronSource.weekday)
		hour, _ := strconv.Atoi(m.cronSource.hour)
		minute, _ := strconv.Atoi(m.cronSource.minute)
		return m, m.createCronSource(m.cronSource.id, m.cronSource.schedule, weekday, hour, minute, m.cronSource.flow)
	case "backspace":
		m.setCronSourceField(trimLastRune(m.cronSourceFieldValue()))
		return m, nil
	default:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		// schedule (field 2) toggles daily/weekly on any keypress rather
		// than accepting free text — a closed two-value choice, not an
		// open field.
		if m.cronSource.field == 2 {
			if m.cronSource.schedule == "daily" {
				m.cronSource.schedule = "weekly"
			} else {
				m.cronSource.schedule = "daily"
			}
			return m, nil
		}
		m.setCronSourceField(m.cronSourceFieldValue() + string(msg.Runes))
		return m, nil
	}
}

func (m Model) cronSourceFieldValue() string {
	switch m.cronSource.field {
	case 0:
		return m.cronSource.id
	case 1:
		return m.cronSource.flow
	case 3:
		return m.cronSource.weekday
	case 4:
		return m.cronSource.hour
	case 5:
		return m.cronSource.minute
	}
	return ""
}

func (m *Model) setCronSourceField(v string) {
	switch m.cronSource.field {
	case 0:
		m.cronSource.id = v
	case 1:
		m.cronSource.flow = v
	case 3:
		m.cronSource.weekday = v
	case 4:
		m.cronSource.hour = v
	case 5:
		m.cronSource.minute = v
	}
}

func (m Model) viewSourceCreate() string {
	var b strings.Builder
	b.WriteString("NEW CRON SOURCE\n\n")
	if m.cronSource.saved != "" {
		fmt.Fprintf(&b, "created: %s\n\nn new source  h home  esc\n", m.cronSource.saved)
		return b.String()
	}
	labels := []string{"id", "flow (workflow path)", "schedule (any key toggles daily/weekly)", "weekday (0=Sun, weekly only)", "hour (0-23)", "minute (0-59)"}
	values := []string{m.cronSource.id, m.cronSource.flow, m.cronSource.schedule, m.cronSource.weekday, m.cronSource.hour, m.cronSource.minute}
	for i, label := range labels {
		if i > m.cronSource.field {
			break
		}
		cursor := "  "
		if i == m.cronSource.field {
			cursor = "▸ "
		}
		fmt.Fprintf(&b, "%s%s: %s", cursor, label, values[i])
		if i == m.cronSource.field && m.mode == ModeINPUT {
			b.WriteString("█")
		}
		b.WriteString("\n")
	}
	if m.cronSource.saveErr != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.cronSource.saveErr)
	}
	if m.cronSource.field < cronSourceFieldCount-1 {
		b.WriteString("\nINPUT  ⏎ next field  esc cancel\n")
	} else {
		b.WriteString("\nINPUT  ⏎ create  esc cancel\n")
	}
	return b.String()
}
