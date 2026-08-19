package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

// failingSecondAppendStore wraps a real Store and fails the second
// AppendIf call it sees — simulating a daemon crash between
// handleCreateRun's two AppendIf calls (decision #9: they are not one
// transaction), leaving TriggerReceived durably recorded and RunStarted
// never attempted.
type failingSecondAppendStore struct {
	eventstore.Store
	lastRunID string
	calls     int
}

func (s *failingSecondAppendStore) AppendIf(ctx context.Context, streamID string, expectedSeq int, evs []domain.Event, meta eventstore.AppendMeta) ([]events.Envelope, error) {
	s.calls++
	s.lastRunID = streamID
	if s.calls >= 2 {
		return nil, errSimulatedCrash
	}
	return s.Store.AppendIf(ctx, streamID, expectedSeq, evs, meta)
}

var errSimulatedCrash = &simulatedCrashError{}

type simulatedCrashError struct{}

func (*simulatedCrashError) Error() string { return "simulated daemon crash before the second append" }

// TestCreateRun_crashBetweenTheTwoAppendsLeavesOnlyTriggerReceived proves
// decision #9's gap is real and bounded: if the daemon dies (or the
// second AppendIf fails for any reason) between appending
// TriggerReceived and appending RunStarted, the run is left with exactly
// one event — never zero, never corrupted, never silently "completed".
func TestCreateRun_crashBetweenTheTwoAppendsLeavesOnlyTriggerReceived(t *testing.T) {
	real := openTestStore(t)
	store := &failingSecondAppendStore{Store: real}
	deps := api.Deps{Store: store}
	mux := api.NewMux(deps)

	body, _ := json.Marshal(map[string]any{"definitionPath": fixIssuePath(t)})
	req := httptest.NewRequest("POST", "/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 (the simulated crash surfacing); body = %s", rec.Code, rec.Body.String())
	}
	if store.lastRunID == "" {
		t.Fatal("expected the first AppendIf to have recorded a run id")
	}

	envs, err := real.Read(context.Background(), store.lastRunID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("len(envs) = %d, want exactly 1 (trigger.received only)", len(envs))
	}
	if envs[0].EventType != "trigger.received" {
		t.Errorf("EventType = %s, want trigger.received", envs[0].EventType)
	}
}
