package eventstore_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

// BenchmarkAppendIf_singleEvent gates single-event append latency
// end-to-end — CAS insert, projection Apply, commit, fsync — at p99 < 5ms
// (AGENTS §1). It uses a real temp-file database, never :memory:, because
// :memory: skips the fsync path this benchmark exists to catch a
// regression in. Each iteration targets a distinct stream so append cost
// is isolated from CAS-conflict-retry cost, a separate correctness concern.
func BenchmarkAppendIf_singleEvent(b *testing.B) {
	registry, err := events.Builtin()
	if err != nil {
		b.Fatalf("Builtin: %v", err)
	}
	dir := b.TempDir()
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(dir, "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "bench", OccurredAt: time.Unix(0, 0)}
	latencies := make([]time.Duration, 0, b.N)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := domain.TriggerReceived{RunID: fmt.Sprintf("bench_%d", i), TriggerRef: "bench", DefinitionRef: "bench", CorrelationID: "bench"}
		start := time.Now()
		if _, err := st.AppendIf(context.Background(), fmt.Sprintf("bench_%d", i), 0, []domain.Event{ev}, meta); err != nil {
			b.Fatalf("AppendIf: %v", err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) == 0 {
		return
	}
	p99 := latencies[int(float64(len(latencies))*0.99)]
	b.ReportMetric(float64(p99.Microseconds()), "p99_us")
	if p99 >= 5*time.Millisecond {
		b.Fatalf("p99 append latency %v exceeds the 5ms gate", p99)
	}
}
