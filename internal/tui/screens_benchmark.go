package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// The Benchmark screen reuses L18's kairos-compare machinery directly
// (internal/cli.Client.Compare) rather than duplicating it. A real
// N-way fork-and-vary flow (09-cli-and-tui.md's full mockup) needs a run
// picker and Engine.Fork wiring this document doesn't build — see
// L15-tui.md's Future work. This is the honest two-run comparison slice:
// type "<runA> <runB>" and see cost/duration/attempts/findings/drift.
type benchmarkFetchedMsg struct {
	result cli.CompareResult
	err    error
}

func (m Model) handleBenchmarkInputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNAV
		return m, nil
	case "enter":
		fields := strings.Fields(m.benchmark.input)
		m.mode = ModeNAV
		if len(fields) != 2 {
			m.statusLine = "type two run ids separated by a space"
			return m, nil
		}
		a, b := fields[0], fields[1]
		return m, func() tea.Msg {
			ctx, cancel := withTimeout(m.ctx)
			defer cancel()
			res, err := m.client.Compare(ctx, a, b)
			return benchmarkFetchedMsg{result: res, err: err}
		}
	case "backspace":
		m.benchmark.input = trimLastRune(m.benchmark.input)
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.benchmark.input += string(msg.Runes)
		}
		return m, nil
	}
}

func (m Model) applyBenchmarkFetched(msg benchmarkFetchedMsg) Model {
	m.benchmark.result = msg.result
	m.benchmark.err = msg.err
	m.benchmark.have = true
	return m
}

func (m Model) viewBenchmark() string {
	var b strings.Builder
	b.WriteString("BENCHMARK\n\n")
	if !m.benchmark.have {
		b.WriteString("i  compare two runs — type \"<runA> <runB>\"\n")
	} else if m.benchmark.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.benchmark.err)
	} else {
		r := m.benchmark.result
		fmt.Fprintf(&b, "%-20s %-12s %-10s %-9s %s\n", "RUN", "STATUS", "ATTEMPTS", "FINDINGS", "DRIFT")
		fmt.Fprintf(&b, "%-20s %-12s %-10d %-9d %v\n", r.A.RunID, r.A.Status, r.A.Attempts, r.A.Findings, r.A.Drifted)
		fmt.Fprintf(&b, "%-20s %-12s %-10d %-9d %v\n", r.B.RunID, r.B.Status, r.B.Attempts, r.B.Findings, r.B.Drifted)
	}
	b.WriteString("\n▏")
	b.WriteString(m.benchmark.input)
	if m.mode == ModeINPUT {
		b.WriteString("█")
	}
	b.WriteString("\n\nNAV  i compare  esc\n")
	return b.String()
}
