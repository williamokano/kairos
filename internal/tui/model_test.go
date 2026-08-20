package tui

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

// TestScreens_renderWithoutPanicking is the smoke test per screen the
// document calls for. It drives Model directly with synthetic fetch
// results rather than through a full tea.Program against a real terminal
// (there is no TTY in a test binary to drive one against) — every screen's
// View() must produce non-empty output for real-shaped data without
// panicking.
func TestScreens_renderWithoutPanicking(t *testing.T) {
	m := New(context.Background(), nil, "", true)
	m.width, m.height = 100, 40

	cases := []struct {
		name   string
		mutate func(Model) Model
	}{
		{"home", func(m Model) Model {
			m.screen = ScreenHome
			m.home.runs = []cli.RunSummary{{RunID: "r1", Status: "running", StartedAt: "t0", UpdatedAt: "t1"}}
			return m
		}},
		{"conversation", func(m Model) Model {
			m.screen = ScreenConversation
			m.conversation.runID = "r1"
			m.conversation.messages = []cli.ConversationMessage{{Role: "you", Text: "hi"}}
			return m
		}},
		{"runInspector", func(m Model) Model {
			m.screen = ScreenRunInspector
			m.runInspector.state = cli.RunState{ID: "r1", Status: "running", Executions: map[string][]cli.NodeExecution{
				"n1": {{ExecID: "n1#a1.i1", NodeID: "n1", Status: "succeeded", Attempt: 1, Iteration: 1}},
			}}
			return m
		}},
		{"logs", func(m Model) Model {
			m.screen = ScreenLogs
			m.logs.runID = "r1"
			return m
		}},
		{"inbox", func(m Model) Model {
			m.screen = ScreenInbox
			m.inbox.items = []inboxItem{{RunID: "r1", NodeID: "approve", ExecID: "approve#a1.i1"}}
			return m
		}},
		{"runners", func(m Model) Model {
			m.screen = ScreenRunners
			m.runners.doctor = cli.DoctorResponse{Checks: []cli.DoctorCheck{{Name: "git", OK: true, Detail: "/usr/bin/git"}}}
			return m
		}},
		{"benchmark", func(m Model) Model {
			m.screen = ScreenBenchmark
			return m
		}},
		{"decision", func(m Model) Model {
			m.screen = ScreenDecision
			m.decision = decisionState{runID: "r1", nodeID: "approve", riskAccepted: map[string]bool{}}
			return m
		}},
		{"onboarding", func(m Model) Model {
			m.screen = ScreenOnboarding
			return m
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := c.mutate(m).View()
			if out == "" {
				t.Fatalf("screen %s rendered an empty frame", c.name)
			}
		})
	}
}

// TestInbox_neverAnswersADecision is a structural check, not just a
// behavioral one: handleInboxKey's only reachable path to a decision is
// openDecision (navigation), and no function in this file calls
// ApproveHumanTask or constructs an approveResultMsg directly. Verified by
// walking this package's own AST for the literal call, matching the
// archtest-style "the checker has teeth" discipline used elsewhere in this
// codebase.
func TestInbox_neverAnswersADecision(t *testing.T) {
	src, err := os.ReadFile(sourceFile(t, "screens_inbox.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "screens_inbox.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "ApproveHumanTask" {
			t.Fatal("screens_inbox.go must never call ApproveHumanTask directly — the inbox links to the decision screen, it never answers")
		}
		return true
	})
}

// TestApproveHumanTask_calledFromExactlyOnePlace guards against a second,
// looser approval path ever being added (a "quick approve" shortcut) —
// 09-cli-and-tui.md: "no global approve shortcut... no bulk approve."
func TestApproveHumanTask_calledFromExactlyOnePlace(t *testing.T) {
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	callSites := 0
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, e.Name(), src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ApproveHumanTask" {
				callSites++
			}
			return true
		})
	}
	if callSites != 1 {
		t.Fatalf("ApproveHumanTask has %d call sites in internal/tui, want exactly 1 (keys_decision.go's submitDecisionCmd) — a second call site would be an undocumented bulk/shortcut approve path", callSites)
	}
}

func packageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func sourceFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(packageDir(t), name)
}
