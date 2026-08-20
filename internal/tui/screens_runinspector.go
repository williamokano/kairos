package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) applyRunStateFetched(msg runStateFetchedMsg) (Model, tea.Cmd) {
	if msg.runID != m.runInspector.runID {
		return m, nil
	}
	m.runInspector.state = msg.state
	m.runInspector.err = msg.err
	return m, nil
}

func (m Model) handleRunInspectorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "c":
		m.navigate(ScreenConversation)
		m.conversation.runID = m.runInspector.runID
		return m, m.fetchConversation(m.runInspector.runID)
	case "l":
		m.navigate(ScreenLogs)
		m.logs.runID = m.runInspector.runID
		return m, nil
	}
	return m, nil
}

func (m Model) viewRunInspector() string {
	var b strings.Builder
	s := m.runInspector.state
	fmt.Fprintf(&b, "RUN INSPECTOR · %s · %s\n\n", s.ID, orDash(s.Status))
	if m.runInspector.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.runInspector.err)
	}

	var nodeIDs []string
	for id := range s.Executions {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	b.WriteString("NODE            EXEC ID              STATUS      ATTEMPT\n")
	for _, id := range nodeIDs {
		for _, e := range s.Executions[id] {
			fmt.Fprintf(&b, "%-15s %-20s %-11s %d\n", id, e.ExecID, e.Status, e.Attempt)
		}
	}
	b.WriteString("\nNAV  c conversation  l logs  esc\n")
	return b.String()
}
