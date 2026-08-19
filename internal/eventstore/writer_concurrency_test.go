package eventstore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
)

// TestArchitecture_singleWriter's runtime half: 50 concurrent AppendIf
// calls against distinct streams must never surface SQLITE_BUSY —
// 06-durability.md: "SQLITE_BUSY is unreachable on the write path" because
// every write funnels through one goroutine.
func TestStore_concurrentAppendsNeverSurfaceSQLITEBUSY(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.AppendIf(ctx, fmt.Sprintf("run_%d", i), 0, []domain.Event{
				domain.TriggerReceived{RunID: fmt.Sprintf("run_%d", i), TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c"},
			}, eventstore.AppendMeta{Actor: "engine", CorrelationID: "c", OccurredAt: time.Unix(0, 0)})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
				t.Fatalf("run_%d: SQLITE_BUSY surfaced on the write path: %v", i, err)
			}
			t.Errorf("run_%d: unexpected error: %v", i, err)
		}
	}
}
