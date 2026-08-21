package eventstore

import (
	"context"
	"database/sql"
	"fmt"
)

// GetAdmissionSpend and SetAdmissionSpend back internal/admission's daily
// spend cap (02-config.md rule 5) with a durable running total per
// calendar day, so a restart mid-day resumes the same total instead of
// silently resetting to zero — see migrations/0004_admission_spend.sql.

func (s *store) GetAdmissionSpend(ctx context.Context, day string) (float64, bool, error) {
	var spent float64
	err := s.readerDB.QueryRowContext(ctx, `SELECT spent_usd FROM admission_spend WHERE day = ?`, day).Scan(&spent)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("querying admission_spend for %s: %w", day, err)
	}
	return spent, true, nil
}

func (s *store) SetAdmissionSpend(ctx context.Context, day string, spentUSD float64) error {
	_, err := s.writerDB.ExecContext(ctx, `
		INSERT INTO admission_spend (day, spent_usd) VALUES (?, ?)
		ON CONFLICT (day) DO UPDATE SET spent_usd = excluded.spent_usd
	`, day, spentUSD)
	if err != nil {
		return fmt.Errorf("upserting admission_spend for %s: %w", day, err)
	}
	return nil
}
