package api_test

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
)

// TestSSE_resumptionHasNoGapOrDuplicate proves decision #8: subscribing to
// the live bus before replaying history, then dedup-by-GlobalSeq draining
// the live channel, means an event appended WHILE a client is mid-replay
// is delivered exactly once — never dropped, never doubled.
func TestSSE_resumptionHasNoGapOrDuplicate(t *testing.T) {
	store := openTestStore(t)
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: "c", OccurredAt: time.Unix(0, 0)}

	// Seed a handful of historical events before the client connects.
	for i := 0; i < 5; i++ {
		runID := "run_" + strconv.Itoa(i)
		if _, err := store.AppendIf(t.Context(), runID, 0, []domain.Event{
			domain.TriggerReceived{RunID: runID, TriggerRef: "t", DefinitionRef: "d", CorrelationID: "c"},
		}, meta); err != nil {
			t.Fatalf("seed AppendIf: %v", err)
		}
	}

	deps := api.Deps{Store: store}
	mux := api.NewMux(deps)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/events?after=0", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Append one more event concurrently with reading the stream — this is
	// the race the dedup logic exists to close.
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = store.AppendIf(t.Context(), "run_late", 0, []domain.Event{
			domain.TriggerReceived{RunID: "run_late", TriggerRef: "t", DefinitionRef: "d", CorrelationID: "c"},
		}, meta)
	}()

	seen := map[int64]int{}
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		line := scanner.Text()
		if !strings.HasPrefix(line, "id: ") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		if err != nil {
			continue
		}
		seen[id]++
		if len(seen) >= 6 {
			break
		}
	}

	if len(seen) != 6 {
		t.Fatalf("saw %d distinct global_seq values, want 6 (5 historical + 1 late): %v", len(seen), seen)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("global_seq %d delivered %d times, want exactly 1", id, count)
		}
	}
}
