package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *store) UpsertProject(ctx context.Context, p Project) error {
	createdAt := p.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.writerDB.ExecContext(ctx, `
		INSERT INTO project (id, name, repo_path, git_backed, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name, repo_path = excluded.repo_path, git_backed = excluded.git_backed
	`, p.ID, p.Name, p.RepoPath, boolToInt(p.GitBacked), nullIfEmpty(p.CreatedBy), createdAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upserting project %s: %w", p.ID, err)
	}
	return nil
}

func (s *store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.readerDB.QueryContext(ctx, `
		SELECT id, name, repo_path, git_backed, created_by, created_at FROM project ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("querying project: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *store) GetProject(ctx context.Context, id string) (Project, bool, error) {
	row := s.readerDB.QueryRowContext(ctx, `
		SELECT id, name, repo_path, git_backed, created_by, created_at FROM project WHERE id = ?`, id)
	p, err := scanProject(row)
	if err == sql.ErrNoRows {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("querying project %s: %w", id, err)
	}
	return p, true, nil
}

func (s *store) GetProjectByName(ctx context.Context, name string) (Project, bool, error) {
	row := s.readerDB.QueryRowContext(ctx, `
		SELECT id, name, repo_path, git_backed, created_by, created_at FROM project WHERE name = ?`, name)
	p, err := scanProject(row)
	if err == sql.ErrNoRows {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("querying project by name %s: %w", name, err)
	}
	return p, true, nil
}

func scanProject(r rowScanner) (Project, error) {
	var (
		p          Project
		gitBacked  int
		createdBy  sql.NullString
		createdAtS string
	)
	if err := r.Scan(&p.ID, &p.Name, &p.RepoPath, &gitBacked, &createdBy, &createdAtS); err != nil {
		return Project{}, err
	}
	p.GitBacked = gitBacked != 0
	p.CreatedBy = createdBy.String
	if t, err := time.Parse(time.RFC3339, createdAtS); err == nil {
		p.CreatedAt = t
	}
	return p, nil
}

func (s *store) UpsertSession(ctx context.Context, sess Session) error {
	now := time.Now().UTC()
	createdAt := sess.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	lastUsedAt := sess.LastUsedAt
	if lastUsedAt.IsZero() {
		lastUsedAt = now
	}
	_, err := s.writerDB.ExecContext(ctx, `
		INSERT INTO session (id, project_id, actor, work_dir, branch, native_session_id, conversation_run_id, last_run_id, run_count, created_by, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			project_id = excluded.project_id, actor = excluded.actor, work_dir = excluded.work_dir,
			branch = excluded.branch, native_session_id = excluded.native_session_id,
			conversation_run_id = excluded.conversation_run_id,
			last_run_id = excluded.last_run_id, run_count = excluded.run_count, last_used_at = excluded.last_used_at
	`, sess.ID, nullIfEmpty(sess.ProjectID), sess.Actor, sess.WorkDir, nullIfEmpty(sess.Branch),
		nullIfEmpty(sess.NativeSessionID), nullIfEmpty(sess.ConversationRunID), nullIfEmpty(sess.LastRunID), sess.RunCount,
		nullIfEmpty(sess.CreatedBy), createdAt.Format(time.RFC3339), lastUsedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upserting session %s: %w", sess.ID, err)
	}
	return nil
}

func (s *store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.readerDB.QueryContext(ctx, `
		SELECT id, project_id, actor, work_dir, branch, native_session_id, conversation_run_id, last_run_id, run_count, created_by, created_at, last_used_at
		FROM session ORDER BY last_used_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying session: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *store) GetSession(ctx context.Context, id string) (Session, bool, error) {
	row := s.readerDB.QueryRowContext(ctx, `
		SELECT id, project_id, actor, work_dir, branch, native_session_id, conversation_run_id, last_run_id, run_count, created_by, created_at, last_used_at
		FROM session WHERE id = ?`, id)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("querying session %s: %w", id, err)
	}
	return sess, true, nil
}

func scanSession(r rowScanner) (Session, error) {
	var (
		sess                                                         Session
		projectID, branch, nativeSessionID, conversationRun, lastRun sql.NullString
		createdBy                                                    sql.NullString
		createdAtS, lastUsedAtS                                      string
	)
	if err := r.Scan(&sess.ID, &projectID, &sess.Actor, &sess.WorkDir, &branch, &nativeSessionID, &conversationRun, &lastRun,
		&sess.RunCount, &createdBy, &createdAtS, &lastUsedAtS); err != nil {
		return Session{}, err
	}
	sess.ProjectID = projectID.String
	sess.Branch = branch.String
	sess.NativeSessionID = nativeSessionID.String
	sess.ConversationRunID = conversationRun.String
	sess.LastRunID = lastRun.String
	sess.CreatedBy = createdBy.String
	if t, err := time.Parse(time.RFC3339, createdAtS); err == nil {
		sess.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, lastUsedAtS); err == nil {
		sess.LastUsedAt = t
	}
	return sess, nil
}

func (s *store) TouchSession(ctx context.Context, id, nativeSessionID, lastRunID string) error {
	// conversation_run_id is set only when it's still NULL (run_count's
	// first bump) — COALESCE keeps every later turn's own value
	// untouched, so the whole chat keeps landing in turn one's run.
	res, err := s.writerDB.ExecContext(ctx, `
		UPDATE session SET
			native_session_id = ?, last_run_id = ?,
			conversation_run_id = COALESCE(conversation_run_id, ?),
			run_count = run_count + 1, last_used_at = ?
		WHERE id = ?`,
		nullIfEmpty(nativeSessionID), nullIfEmpty(lastRunID), nullIfEmpty(lastRunID),
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("touching session %s: %w", id, err)
	}
	return mustAffectOne(res, "session", id)
}
