package tasksource_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/tasksource"
)

func TestCreateRun_setsTriggerRefFromRequest(t *testing.T) {
	st := openStore(t)
	runID, status, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
		DefinitionRef: demoFlow(t), TriggerRef: "cron:nightly", Actor: "trigger:cron",
	}, tasksource.QueueLimits{})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status != domain.RunRunning {
		t.Fatalf("status = %s, want running", status)
	}
	envs, err := st.Read(context.Background(), runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	trigger, ok := envs[0].Event.(domain.TriggerReceived)
	if !ok {
		t.Fatalf("first event = %T, want TriggerReceived", envs[0].Event)
	}
	if trigger.TriggerRef != "cron:nightly" {
		t.Errorf("TriggerRef = %q, want cron:nightly", trigger.TriggerRef)
	}
}

func TestCreateRun_badDefinitionIsAValidationError(t *testing.T) {
	st := openStore(t)
	_, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
		DefinitionRef: "/no/such/file.yaml", TriggerRef: "cli:kairos-run",
	}, tasksource.QueueLimits{})
	if err == nil {
		t.Fatal("expected an error")
	}
	var verr *tasksource.ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("error = %v, want a *ValidationError", err)
	}
}

func TestCreateRun_rejectsPastMaxQueued(t *testing.T) {
	st := openStore(t)
	limits := tasksource.QueueLimits{MaxQueued: 1}

	if _, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
		DefinitionRef: demoFlow(t), TriggerRef: "poll:x:1",
	}, limits); err != nil {
		t.Fatalf("first CreateRun: %v", err)
	}

	_, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
		DefinitionRef: demoFlow(t), TriggerRef: "poll:x:2",
	}, limits)
	if !errors.Is(err, tasksource.ErrQueueFull) {
		t.Errorf("second CreateRun error = %v, want ErrQueueFull", err)
	}
}

func TestTriggerRun_sameDedupeKeyProducesExactlyOneRun(t *testing.T) {
	st := openStore(t)
	req := tasksource.CreateRunRequest{DefinitionRef: demoFlow(t), TriggerRef: "poll:src:item1"}

	runID1, created1, err := tasksource.TriggerRun(context.Background(), st, "dedupe-key-1", "src", "item1", req, tasksource.QueueLimits{})
	if err != nil {
		t.Fatalf("first TriggerRun: %v", err)
	}
	if !created1 {
		t.Fatal("expected the first call to create a run")
	}

	runID2, created2, err := tasksource.TriggerRun(context.Background(), st, "dedupe-key-1", "src", "item1", req, tasksource.QueueLimits{})
	if err != nil {
		t.Fatalf("second TriggerRun: %v", err)
	}
	if created2 {
		t.Error("expected the second call to NOT create a run")
	}
	if runID2 != runID1 {
		t.Errorf("runID2 = %s, want %s (same run)", runID2, runID1)
	}

	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1", len(runs))
	}
}

func TestTriggerRun_concurrentSameKeyProducesExactlyOneRun(t *testing.T) {
	st := openStore(t)
	req := tasksource.CreateRunRequest{DefinitionRef: demoFlow(t), TriggerRef: "poll:src:racy"}

	const n = 10
	var wg sync.WaitGroup
	created := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, c, err := tasksource.TriggerRun(context.Background(), st, "racy-key", "src", "racy", req, tasksource.QueueLimits{})
			// A narrow, real race is allowed to surface as an error (see
			// dedupe.go's doc comment) rather than a wrong answer;
			// what must never happen is more than one call reporting
			// created=true.
			_ = err
			created[i] = c
		}(i)
	}
	wg.Wait()

	count := 0
	for _, c := range created {
		if c {
			count++
		}
	}
	if count != 1 {
		t.Errorf("created count = %d, want exactly 1", count)
	}
	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1", len(runs))
	}
}
