package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/apispec"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

// nopStore is a minimal eventstore.Store good enough to prove every route
// is reachable — not a durability test (that's internal/eventstore's job).
type nopStore struct{}

func (nopStore) AppendIf(context.Context, string, int, []domain.Event, eventstore.AppendMeta) ([]events.Envelope, error) {
	return nil, nil
}
func (nopStore) Read(context.Context, string) ([]events.Envelope, error) { return nil, nil }
func (nopStore) ReadAll(context.Context, int64, int) ([]events.Envelope, error) {
	return nil, nil
}
func (nopStore) Subscribe(context.Context) (<-chan events.Envelope, func()) {
	ch := make(chan events.Envelope)
	return ch, func() {}
}
func (nopStore) Verify(context.Context) (eventstore.VerifyReport, error) {
	return eventstore.VerifyReport{}, nil
}
func (nopStore) Rebuild(context.Context) error { return nil }
func (nopStore) ListRuns(context.Context, *domain.RunStatus) ([]eventstore.RunSummary, error) {
	return nil, nil
}
func (nopStore) GetRunState(context.Context, string) (domain.RunState, bool, error) {
	return domain.RunState{}, false, nil
}
func (nopStore) Close() error                                             { return nil }
func (nopStore) UpsertSource(context.Context, eventstore.Source) error    { return nil }
func (nopStore) ListSources(context.Context) ([]eventstore.Source, error) { return nil, nil }
func (nopStore) GetSource(context.Context, string) (eventstore.Source, bool, error) {
	return eventstore.Source{}, false, nil
}
func (nopStore) SetSourceEnabled(context.Context, string, bool) error                   { return nil }
func (nopStore) SetSourceHealth(context.Context, string, eventstore.SourceHealth) error { return nil }
func (nopStore) GetSourceCursor(context.Context, string) (string, string, bool, error) {
	return "", "", false, nil
}
func (nopStore) SetSourceCursor(context.Context, string, string, string) error { return nil }
func (nopStore) DedupeTrigger(context.Context, string, string, string, time.Time) (string, bool, error) {
	return "", true, nil
}
func (nopStore) RecordTriggerRun(context.Context, string, string) error { return nil }

// TestAPI_everyOpIsRegistered asserts every apispec.Op is actually
// reachable on the mux NewMux builds — the "routes are registered" half
// of TestUI_everyCallHasCLICounterpart's parity guarantee. A route that
// doesn't match falls through to Go's default "404 page not found" mux
// response; anything else (including a domain-level 404 our own
// writeError produces, which has a JSON body) proves the pattern matched.
func TestAPI_everyOpIsRegistered(t *testing.T) {
	deps := api.Deps{Store: nopStore{}}
	mux := api.NewMux(deps)

	if len(apispec.Ops) == 0 {
		t.Fatal("apispec.Ops is empty — the parity check would be vacuous")
	}

	for _, op := range apispec.Ops {
		path := strings.ReplaceAll(op.Path, "{id}", "test-id")
		req := httptest.NewRequest(op.Method, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == 404 && strings.Contains(rec.Body.String(), "404 page not found") {
			t.Errorf("%s %s: not registered (default mux 404)", op.Method, op.Path)
		}
	}
}
