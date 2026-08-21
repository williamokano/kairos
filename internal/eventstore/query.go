package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
)

// RunSummary is one row of the run_index projection — the narrow shape
// `kairos ls` needs without deserializing a full RunState blob.
type RunSummary struct {
	RunID     string
	Status    domain.RunStatus
	StartedAt string // RFC3339Nano, or "" if never set
	UpdatedAt string
}

func (s *store) ListRuns(ctx context.Context, status *domain.RunStatus) ([]RunSummary, error) {
	query := `SELECT run_id, status, started_at, updated_at FROM run_index`
	args := []any{}
	if status != nil {
		query += ` WHERE status = ?`
		args = append(args, string(*status))
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.readerDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying run_index: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RunSummary
	for rows.Next() {
		var (
			runID, statusStr, updatedAt string
			startedAt                   sql.NullString
		)
		if err := rows.Scan(&runID, &statusStr, &startedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning run_index row: %w", err)
		}
		out = append(out, RunSummary{
			RunID:     runID,
			Status:    domain.RunStatus(statusStr),
			StartedAt: startedAt.String,
			UpdatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating run_index rows: %w", err)
	}
	return out, nil
}

// OpenHumanTask is one row of human_task_index — see
// HumanTaskIndexProjection's doc comment.
type OpenHumanTask struct {
	RunID, NodeID string
	Kind          string // "human" | "effect_confirm"
	OpenedAt      string // RFC3339Nano
}

func (s *store) ListOpenHumanTasks(ctx context.Context) ([]OpenHumanTask, error) {
	rows, err := s.readerDB.QueryContext(ctx, `SELECT run_id, node_id, kind, opened_at FROM human_task_index ORDER BY opened_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying human_task_index: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []OpenHumanTask
	for rows.Next() {
		var t OpenHumanTask
		if err := rows.Scan(&t.RunID, &t.NodeID, &t.Kind, &t.OpenedAt); err != nil {
			return nil, fmt.Errorf("scanning human_task_index row: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating human_task_index rows: %w", err)
	}
	return out, nil
}

func (s *store) GetRunState(ctx context.Context, runID string) (domain.RunState, bool, error) {
	var stateJSON string
	err := s.readerDB.QueryRowContext(ctx, `SELECT state_json FROM run_state_projection WHERE run_id = ?`, runID).Scan(&stateJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunState{}, false, nil
	}
	if err != nil {
		return domain.RunState{}, false, fmt.Errorf("reading run state for %s: %w", runID, err)
	}
	var state domain.RunState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return domain.RunState{}, false, fmt.Errorf("unmarshalling run state for %s: %w", runID, err)
	}
	return state, true, nil
}
