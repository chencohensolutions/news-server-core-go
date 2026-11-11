package store

import (
	"context"
	"time"
)

// HiddenSources are the sites user hid from their feed.
func (s *Store) HiddenSources(ctx context.Context, user string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id FROM user_hidden_sources WHERE user = ?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetSourceHidden hides sourceID from user's feed, or shows it again.
func (s *Store) SetSourceHidden(ctx context.Context, user, sourceID string, hidden bool) error {
	if hidden {
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO user_hidden_sources (user, source_id) VALUES (?, ?)`, user, sourceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_hidden_sources WHERE user = ? AND source_id = ?`, user, sourceID)
	return err
}

// Read is an article a user read, and when.
type Read struct {
	URL    string
	ReadAt time.Time
}

// MarkRead records that user read urls at now; an article read before keeps its first time.
func (s *Store) MarkRead(ctx context.Context, user string, urls []string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, url := range urls {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_reads (user, url, read_at) VALUES (?, ?, ?)`,
			user, url, now.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Reads are the articles user read after since (all of them for the zero time), oldest first.
func (s *Store) Reads(ctx context.Context, user string, since time.Time) ([]Read, error) {
	after := int64(-1)
	if !since.IsZero() {
		after = since.UnixMilli()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT url, read_at FROM user_reads WHERE user = ? AND read_at > ? ORDER BY read_at`, user, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Read
	for rows.Next() {
		var r Read
		var at int64
		if err := rows.Scan(&r.URL, &at); err != nil {
			return nil, err
		}
		r.ReadAt = time.UnixMilli(at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ForgetUser drops a removed user's choices and reads.
func (s *Store) ForgetUser(ctx context.Context, user string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM user_hidden_sources WHERE user = ?`, user); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_reads WHERE user = ?`, user)
	return err
}
