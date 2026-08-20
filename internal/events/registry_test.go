package events_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
)

func TestRegistry_registerTwiceForSameVersionIsAnError(t *testing.T) {
	r := events.NewRegistry()
	schema := []byte(`{"type":"object"}`)
	if err := r.Register("x.y", 1, schema, func() domain.Event { return &domain.RunRejected{} }); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register("x.y", 1, schema, func() domain.Event { return &domain.RunRejected{} }); err == nil {
		t.Fatal("expected an error registering the same (type, version) twice")
	}
}

func TestRegistry_decodeRejectsPayloadFailingSchema(t *testing.T) {
	r, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	_, err = r.Decode("run.rejected", 1, []byte(`{"RunID": "run_1"}`)) // missing required Reason
	if err == nil {
		t.Fatal("expected schema validation to reject a payload missing a required field")
	}
}

func TestRegistry_decodeReturnsTheValueTypeAdvanceSwitchesOn(t *testing.T) {
	r, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	ev, err := r.Decode("run.rejected", 1, []byte(`{"RunID": "run_1", "Reason": "preflight"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := ev.(domain.RunRejected); !ok {
		t.Fatalf("Decode returned %T, want domain.RunRejected (value, not pointer)", ev)
	}
}

func TestRegistry_currentVersionReportsTheNewestRegistered(t *testing.T) {
	r, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	v, ok := r.CurrentVersion("trigger.received")
	if !ok || v != 1 {
		t.Errorf("CurrentVersion = %d, %v; want 1, true", v, ok)
	}
	if _, ok := r.CurrentVersion("no.such.type"); ok {
		t.Error("expected CurrentVersion to report false for an unknown type")
	}
}

func TestBuiltin_registersAllFortyFiveEventTypes(t *testing.T) {
	r, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if got := len(r.Types()); got != 45 {
		t.Errorf("len(Types()) = %d, want 45 (16 from L01 + 4 system-stream events from L05 + 4 run-scoped actor-invocation events from L08 + 2 log-backpressure events from L09 + 1 constraint-evaluation event from L10 + 3 waiver/effect-confirmation events from L11 + 1 conversation-message event from L14 + 8 effect state-machine events from L12 + 1 secret-access event from L16 + 2 spawn/join bookkeeping events from L17 + 3 fork/snapshot bookkeeping events from L18)", got)
	}
}
