package engine

import "testing"

func TestParseBoundedStrategy(t *testing.T) {
	n, err := parseBoundedStrategy("bounded(3)")
	if err != nil || n != 3 {
		t.Fatalf("parseBoundedStrategy(bounded(3)) = %d, %v; want 3, nil", n, err)
	}
	if _, err := parseBoundedStrategy("unbounded"); err == nil {
		t.Fatal("expected an error for a non-bounded(N) strategy")
	}
}

func TestSpawnTriggerRefRoundTrips(t *testing.T) {
	ref := formatSpawnTriggerRef("run_parent", "fanout", "fanout#a1.i1", 2)
	parentRunID, nodeID, execID, index, ok := parseSpawnTriggerRef(ref)
	if !ok {
		t.Fatalf("parseSpawnTriggerRef(%q) did not recognise its own format", ref)
	}
	if parentRunID != "run_parent" || nodeID != "fanout" || execID != "fanout#a1.i1" || index != 2 {
		t.Errorf("parseSpawnTriggerRef(%q) = %q, %q, %q, %d; want run_parent, fanout, fanout#a1.i1, 2",
			ref, parentRunID, nodeID, execID, index)
	}
}

func TestSpawnTriggerRefRejectsUnrelatedRefs(t *testing.T) {
	for _, ref := range []string{"cli:kairos-run", "inbox:abc123", "poll:src:item", "", "spawn:only-one-part"} {
		if _, _, _, _, ok := parseSpawnTriggerRef(ref); ok {
			t.Errorf("parseSpawnTriggerRef(%q) = ok, want not-ok", ref)
		}
	}
}

func TestResolveChildDefinitionPath(t *testing.T) {
	got, err := resolveChildDefinitionPath("/repo/workflows/parent.yaml", "implement-task")
	if err != nil {
		t.Fatalf("resolveChildDefinitionPath: %v", err)
	}
	if want := "/repo/workflows/implement-task.yaml"; got != want {
		t.Errorf("resolveChildDefinitionPath = %q, want %q", got, want)
	}
	if _, err := resolveChildDefinitionPath("/repo/workflows/parent.yaml", ""); err == nil {
		t.Error("expected an error for an empty spawn.workflow")
	}
}
