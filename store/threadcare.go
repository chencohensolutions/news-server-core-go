package store

import (
	"context"
	"database/sql"
	"errors"
)

// The weekly tidy-up of the threads (summary/tidy.go): a thread merged into another leaves an alias, so its id still
// finds the thread it is part of now.

// MergeThreads moves gone's stories and relations into keep, gives keep name and summary, and deletes gone, leaving
// an alias from gone (and from what was merged into it before) to keep.
func (s *Store) MergeThreads(ctx context.Context, keep, gone, name, summary string) error {
	if keep == gone {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`INSERT OR IGNORE INTO story_threads (story, thread) SELECT story, ?1 FROM story_threads WHERE thread = ?2`,
		`DELETE FROM story_threads WHERE thread = ?2`,
		`INSERT OR IGNORE INTO thread_links (a, b) SELECT ?1, b FROM thread_links WHERE a = ?2 AND b != ?1`,
		`INSERT OR IGNORE INTO thread_links (a, b) SELECT a, ?1 FROM thread_links WHERE b = ?2 AND a != ?1`,
		`DELETE FROM thread_links WHERE a = ?2 OR b = ?2`,
		`DELETE FROM threads WHERE id = ?2`,
		`UPDATE thread_aliases SET thread = ?1 WHERE thread = ?2`,
		`INSERT OR REPLACE INTO thread_aliases (alias, thread) VALUES (?2, ?1)`,
	} {
		if _, err := tx.ExecContext(ctx, q, keep, gone); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET name = ?, summary = ? WHERE id = ?`, name, summary, keep); err != nil {
		return err
	}
	if err := refreshSpan(ctx, tx, keep); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveStories moves stories from thread from to thread to (which must exist), and sets both threads' spans from
// their stories.
func (s *Store) MoveStories(ctx context.Context, from, to string, stories []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range stories {
		if _, err := tx.ExecContext(ctx, `DELETE FROM story_threads WHERE story = ? AND thread = ?`, id, from); err != nil {
			return err
		}
		if to != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO story_threads (story, thread) VALUES (?, ?)`, id, to); err != nil {
				return err
			}
		}
	}
	for _, t := range []string{from, to} {
		if t != "" {
			if err := refreshSpan(ctx, tx, t); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// refreshSpan sets thread id's span from its stories: its first story's first time, its latest one's last.
func refreshSpan(ctx context.Context, tx *sql.Tx, id string) error {
	var first, last sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT MIN(r.first_written), MAX(r.written_at) FROM story_records r
		JOIN story_threads st ON st.story = r.id WHERE st.thread = ?`, id).Scan(&first, &last)
	if err != nil || !first.Valid {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE threads SET started_at = ?, updated_at = ? WHERE id = ?`, first.Int64, last.Int64, id)
	return err
}

// ThreadAlias is the thread id was merged into, or "".
func (s *Store) ThreadAlias(ctx context.Context, id string) (string, error) {
	var thread string
	err := s.db.QueryRowContext(ctx, `SELECT thread FROM thread_aliases WHERE alias = ?`, id).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return thread, err
}

// DeleteNarrative forgets ref's vector (the kind's, in space).
func (s *Store) DeleteNarrative(ctx context.Context, space, kind, ref string) error {
	var rowid int64
	var dims int
	err := s.db.QueryRowContext(ctx, `SELECT rowid, dims FROM narratives WHERE space = ? AND kind = ? AND ref = ?`, space, kind, ref).Scan(&rowid, &dims)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+vecTable(dims)+` WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM narratives WHERE rowid = ?`, rowid)
	return err
}

// RenameThread gives thread id name and summary.
func (s *Store) RenameThread(ctx context.Context, id, name, summary string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE threads SET name = ?, summary = ? WHERE id = ?`, name, summary, id)
	return err
}
