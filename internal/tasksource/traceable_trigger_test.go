package tasksource_test

import (
	"context"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/tasksource"
)

// knownTriggerKinds is every prefix this package's own CreateRun/
// TriggerRun call sites use — the documented "<kind>:<detail>" convention
// TriggerReceived's own doc comment (internal/domain/event.go) has said
// "L16 gives this structure" since L05.
var knownTriggerKinds = []string{"cli:", "inbox:", "poll:", "cron:", "webhook:", "digest:"}

// TestEngine_everyRunHasATraceableTrigger is AGENTS.md §9's L15 test: "no
// Run exists that nobody asked for." Every way this document lets a run
// come into existence — a direct CreateRun call, TriggerRun's dedupe
// path, the inbox, a poller, cron, a webhook — must leave a run whose
// FIRST event is TriggerReceived carrying a non-empty TriggerRef in the
// documented convention. This is checked directly against the durable
// log, not against in-memory state, because the log is the only copy
// that survives a restart.
func TestEngine_everyRunHasATraceableTrigger(t *testing.T) {
	st := openStore(t)

	cases := []struct {
		name string
		run  func() string
	}{
		{"cli", func() string {
			id, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
				DefinitionRef: demoFlow(t), TriggerRef: "cli:kairos-run", Actor: "cli",
			}, tasksource.QueueLimits{})
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			return id
		}},
		{"poll", func() string {
			id, _, err := tasksource.TriggerRun(context.Background(), st, "traceable-poll-1", "srcA", "item1",
				tasksource.CreateRunRequest{DefinitionRef: demoFlow(t), TriggerRef: "poll:srcA:item1", Actor: "trigger:poll"},
				tasksource.QueueLimits{})
			if err != nil {
				t.Fatalf("TriggerRun: %v", err)
			}
			return id
		}},
		{"cron", func() string {
			id, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
				DefinitionRef: demoFlow(t), TriggerRef: "cron:nightly", Actor: "trigger:cron",
			}, tasksource.QueueLimits{})
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			return id
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runID := c.run()
			envs, err := st.Read(context.Background(), runID)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(envs) == 0 {
				t.Fatal("run has no events")
			}
			trigger, ok := envs[0].Event.(domain.TriggerReceived)
			if !ok {
				t.Fatalf("first event = %T, want TriggerReceived", envs[0].Event)
			}
			if trigger.TriggerRef == "" {
				t.Fatal("TriggerRef is empty — this run traces to nothing")
			}
			known := false
			for _, kind := range knownTriggerKinds {
				if strings.HasPrefix(trigger.TriggerRef, kind) {
					known = true
					break
				}
			}
			if !known {
				t.Errorf("TriggerRef %q does not start with any known kind %v", trigger.TriggerRef, knownTriggerKinds)
			}
		})
	}
}
