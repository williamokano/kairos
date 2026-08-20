package admission_test

import (
	"sync"
	"testing"

	"github.com/williamokano/kairos/internal/admission"
)

func TestTryAdmit_drainingDeniesEverything(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 4})
	m.SetDraining(true)

	d := m.TryAdmit(admission.Request{NodeSlot: true})
	if d.Outcome != admission.Denied || d.Reason != "shutting down" {
		t.Fatalf("got %+v, want Denied/shutting down", d)
	}
}

func TestTryAdmit_nodeSlotsExhaustedQueuesUnderMaxQueued(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 1, MaxQueued: 10})

	g := m.TryAdmit(admission.Request{NodeSlot: true})
	if g.Outcome != admission.Granted {
		t.Fatalf("first request: got %+v, want Granted", g)
	}

	q := m.TryAdmit(admission.Request{NodeSlot: true})
	if q.Outcome != admission.Queued {
		t.Fatalf("second request: got %+v, want Queued", q)
	}
	if q.Reason != "1 of 1 slots busy" {
		t.Errorf("reason = %q, want the verbatim busy string", q.Reason)
	}

	m.Release(g.Claims)
	g2 := m.TryAdmit(admission.Request{NodeSlot: true})
	if g2.Outcome != admission.Granted {
		t.Fatalf("after release: got %+v, want Granted", g2)
	}
}

func TestTryAdmit_pastMaxQueuedRejectsInsteadOfQueueing(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 1, MaxQueued: 2})
	_ = m.TryAdmit(admission.Request{NodeSlot: true}) // holds the only slot

	d := m.TryAdmit(admission.Request{NodeSlot: true, QueueDepth: 2})
	if d.Outcome != admission.Denied {
		t.Fatalf("got %+v, want Denied once QueueDepth >= MaxQueued", d)
	}
}

func TestTryAdmit_modelSlotsExhausted(t *testing.T) {
	m := admission.New(admission.Config{ModelSlots: map[string]int{"strong": 2}, MaxQueued: 10})

	a := m.TryAdmit(admission.Request{ModelClass: "strong"})
	b := m.TryAdmit(admission.Request{ModelClass: "strong"})
	if a.Outcome != admission.Granted || b.Outcome != admission.Granted {
		t.Fatalf("expected both grants within capacity: a=%+v b=%+v", a, b)
	}

	c := m.TryAdmit(admission.Request{ModelClass: "strong"})
	if c.Outcome != admission.Queued {
		t.Fatalf("third claim: got %+v, want Queued", c)
	}
	if c.Reason != "2 of 2 strong processes busy" {
		t.Errorf("reason = %q", c.Reason)
	}
}

func TestTryAdmit_dailyBudgetCapDenies(t *testing.T) {
	m := admission.New(admission.Config{DailyUSD: 10})

	a := m.TryAdmit(admission.Request{EstimatedCostUSD: 8})
	if a.Outcome != admission.Granted {
		t.Fatalf("first spend: got %+v, want Granted", a)
	}
	b := m.TryAdmit(admission.Request{EstimatedCostUSD: 5})
	if b.Outcome != admission.Denied {
		t.Fatalf("second spend over cap: got %+v, want Denied", b)
	}
}

func TestTryAdmit_allOrNothingClaimsLeakNoPartialGrant(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 5, ModelSlots: map[string]int{"strong": 1}})

	// Exhaust the model pool first, leaving node slots wide open.
	first := m.TryAdmit(admission.Request{NodeSlot: true, ModelClass: "strong"})
	if first.Outcome != admission.Granted {
		t.Fatalf("first request: got %+v, want Granted", first)
	}

	// A second request needs both a node slot (available) and the same
	// model class (exhausted) — it must get NEITHER, not a leaked node
	// slot with no matching model claim.
	second := m.TryAdmit(admission.Request{NodeSlot: true, ModelClass: "strong", QueueDepth: 999})
	if second.Outcome == admission.Granted {
		t.Fatalf("second request should not be granted: %+v", second)
	}

	// Prove no node slot leaked: four more requests needing only a node
	// slot must all succeed (5 total slots, 1 held by `first`).
	granted := 0
	for i := 0; i < 4; i++ {
		d := m.TryAdmit(admission.Request{NodeSlot: true})
		if d.Outcome == admission.Granted {
			granted++
		}
	}
	if granted != 4 {
		t.Errorf("granted = %d, want 4 (a leaked partial claim would reduce this)", granted)
	}
}

func TestTryAdmit_workspaceWriteLockIsExclusiveUnderConcurrency(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 100, MaxQueued: 1000})

	const attempts = 50
	var wg sync.WaitGroup
	results := make([]admission.Outcome, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := m.TryAdmit(admission.Request{NodeSlot: true, WorkspaceKey: "repo:orders", QueueDepth: i})
			results[i] = d.Outcome
		}(i)
	}
	wg.Wait()

	granted := 0
	for _, o := range results {
		if o == admission.Granted {
			granted++
		}
	}
	if granted != 1 {
		t.Errorf("granted = %d for the same workspace key concurrently, want exactly 1", granted)
	}
}

func TestTryAdmit_workspaceLockReleasesAndReadmitsSequentially(t *testing.T) {
	m := admission.New(admission.Config{NodeSlots: 10})

	a := m.TryAdmit(admission.Request{NodeSlot: true, WorkspaceKey: "repo:orders"})
	if a.Outcome != admission.Granted {
		t.Fatalf("first writer: got %+v, want Granted", a)
	}
	b := m.TryAdmit(admission.Request{NodeSlot: true, WorkspaceKey: "repo:orders"})
	if b.Outcome != admission.Queued {
		t.Fatalf("second writer while first holds the lock: got %+v, want Queued", b)
	}

	m.Release(a.Claims)
	c := m.TryAdmit(admission.Request{NodeSlot: true, WorkspaceKey: "repo:orders"})
	if c.Outcome != admission.Granted {
		t.Fatalf("after release: got %+v, want Granted", c)
	}
}
