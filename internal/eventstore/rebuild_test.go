package eventstore_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/store/sqlite"
)

func TestStore_rebuildIsByteIdentical(t *testing.T) {
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kairos.db")

	ctx := context.Background()
	st, err := eventstore.Open(ctx, eventstore.Config{
		Path:     dbPath,
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}
	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "$succeed", domain.OnFailure: "$fail", domain.OnTimeout: "$fail"},
		},
	}
	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, "run_1", 1, []domain.Event{
		domain.RunStarted{RunID: "run_1", Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	before := hashProjectionTables(t, dbPath)

	if err := st.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	after := hashProjectionTables(t, dbPath)

	if before != after {
		t.Errorf("projection tables changed after Rebuild: before=%s after=%s", before, after)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// hashProjectionTables hashes the full contents of the projection tables,
// ordered deterministically, as a cheap byte-identity check.
func hashProjectionTables(t *testing.T, dbPath string) string {
	t.Helper()
	db, err := sqlite.Open(dbPath, sqlite.ModeReader)
	if err != nil {
		t.Fatalf("opening for hash: %v", err)
	}
	defer func() { _ = db.Close() }()

	h := sha256.New()
	for _, q := range []string{
		`SELECT run_id, state_json, status FROM run_state_projection ORDER BY run_id`,
		`SELECT run_id, status, started_at FROM run_index ORDER BY run_id`,
	} {
		hashQuery(t, db, q, h)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashQuery(t *testing.T, db *sql.DB, query string, h io.Writer) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("querying: %v", err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		_, _ = fmt.Fprintf(h, "%v\x00", vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}
