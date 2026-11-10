// Package store keeps what the server read from the sites in one SQLite file (modernc.org/sqlite, no cgo): the
// headlines with when each was first and last seen, the articles with the page each was read from (so a changed
// config can read them again without a download), the articles whose download failed and when to try again, and
// each site's last run.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS headlines (
	url TEXT PRIMARY KEY,
	source_id TEXT NOT NULL,
	item TEXT NOT NULL,          -- scrape.ArticleSummary as JSON
	published_at TEXT,           -- RFC 3339 UTC, or NULL
	first_seen INTEGER NOT NULL, -- Unix milliseconds
	last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS headlines_last_seen ON headlines(last_seen);
CREATE TABLE IF NOT EXISTS articles (
	url TEXT PRIMARY KEY,
	source_id TEXT NOT NULL,
	article TEXT NOT NULL,       -- scrape.Article as JSON
	page TEXT NOT NULL,          -- the HTML it was read from
	fetched_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS failures (
	url TEXT PRIMARY KEY,
	times INTEGER NOT NULL,
	next_try INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_runs (
	source_id TEXT PRIMARY KEY,
	ran_at INTEGER NOT NULL,
	ok_at INTEGER,
	error TEXT,
	headlines INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS story_vectors (
	space TEXT NOT NULL,         -- the model and the story text, e.g. "bge-m3/title_and_subtitle"
	key TEXT NOT NULL,           -- the headline's URL and texts
	vector BLOB NOT NULL,        -- little-endian float32s
	PRIMARY KEY (space, key)
) WITHOUT ROWID;
`

// Store is the database. Safe for concurrent use.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// One writer at a time is SQLite's rule; one connection keeps it simple and avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func millis(t time.Time) int64 { return t.UnixMilli() }

// Headline is a stored headline and when the server first and last saw it on its site.
type Headline struct {
	scrape.ArticleSummary
	FirstSeen time.Time
	LastSeen  time.Time
}

// SaveHeadlines records items as on their site's headline pages at now: new ones are first seen now, known ones keep
// their first sighting and what the article filled in (FilledFrom), with the list page's fields updated.
func (s *Store) SaveHeadlines(ctx context.Context, items []scrape.ArticleSummary, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		var known string
		err := tx.QueryRowContext(ctx, `SELECT item FROM headlines WHERE url = ?`, item.URL).Scan(&known)
		if err == nil {
			var before scrape.ArticleSummary
			if json.Unmarshal([]byte(known), &before) == nil {
				// What the article gave stays: the next list read has only the list's shorter version.
				item.Subtitle, item.Video = before.Subtitle, before.Video
				if before.Subtitle != "" || before.Video {
					item = keepArticleFields(item, before)
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		data, _ := json.Marshal(item)
		if _, err := tx.ExecContext(ctx, `INSERT INTO headlines (url, source_id, item, published_at, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(url) DO UPDATE SET item = excluded.item, published_at = excluded.published_at,
				last_seen = excluded.last_seen`,
			item.URL, item.SourceID, string(data), nullable(item.PublishedAt), millis(now), millis(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// keepArticleFields keeps a filled headline's article data over the list page's.
func keepArticleFields(list, filled scrape.ArticleSummary) scrape.ArticleSummary {
	out := filled
	// The list page may have learned a time or a category it didn't have.
	if out.PublishedAt == "" {
		out.PublishedAt = list.PublishedAt
	}
	if out.Category == "" {
		out.Category = list.Category
	}
	if out.Summary == "" {
		out.Summary = list.Summary
	}
	return out
}

// UpdateHeadline replaces a known headline's data (the article filled it in), keeping its sightings.
func (s *Store) UpdateHeadline(ctx context.Context, item scrape.ArticleSummary) error {
	data, _ := json.Marshal(item)
	_, err := s.db.ExecContext(ctx, `UPDATE headlines SET item = ?, published_at = ? WHERE url = ?`,
		string(data), nullable(item.PublishedAt), item.URL)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Headlines are those seen on their site at or after since, newest sighting first.
func (s *Store) Headlines(ctx context.Context, since time.Time) ([]Headline, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item, first_seen, last_seen FROM headlines WHERE last_seen >= ?
		ORDER BY last_seen DESC, first_seen DESC, url`, millis(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Headline
	for rows.Next() {
		var data string
		var first, last int64
		if err := rows.Scan(&data, &first, &last); err != nil {
			return nil, err
		}
		var h Headline
		if err := json.Unmarshal([]byte(data), &h.ArticleSummary); err != nil {
			return nil, err
		}
		h.FirstSeen, h.LastSeen = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// HasArticle is whether url's article is stored.
func (s *Store) HasArticle(ctx context.Context, url string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM articles WHERE url = ?`, url).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Article is url's stored article, or nil.
func (s *Store) Article(ctx context.Context, url string) (*scrape.Article, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT article FROM articles WHERE url = ?`, url).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a scrape.Article
	return &a, json.Unmarshal([]byte(data), &a)
}

// SaveArticle stores a, read from page at now, and forgets any failure of its url.
func (s *Store) SaveArticle(ctx context.Context, a *scrape.Article, page string, now time.Time) error {
	data, _ := json.Marshal(a)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO articles (url, source_id, article, page, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET article = excluded.article, page = excluded.page, fetched_at = excluded.fetched_at`,
		a.URL, a.SourceID, string(data), page, millis(now)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM failures WHERE url = ?`, a.URL); err != nil {
		return err
	}
	return tx.Commit()
}

// Failure is how often in a row url's download failed and when it may be tried again; zero when it hasn't failed.
func (s *Store) Failure(ctx context.Context, url string) (times int, nextTry time.Time, err error) {
	var next int64
	err = s.db.QueryRowContext(ctx, `SELECT times, next_try FROM failures WHERE url = ?`, url).Scan(&times, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	return times, time.UnixMilli(next).UTC(), err
}

// SetFailure records url's download as failed for the given time in a row, to be tried again at nextTry.
func (s *Store) SetFailure(ctx context.Context, url string, times int, nextTry time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO failures (url, times, next_try) VALUES (?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET times = excluded.times, next_try = excluded.next_try`, url, times, millis(nextTry))
	return err
}

// SiteRun is a site's last headline read.
type SiteRun struct {
	SourceID  string
	RanAt     time.Time
	OKAt      time.Time // zero when it never succeeded
	Error     string    // "" when the last run succeeded
	Headlines int
}

// SaveSiteRun records sourceID's headline read at now: its count, or runErr.
func (s *Store) SaveSiteRun(ctx context.Context, sourceID string, headlines int, runErr error, now time.Time) error {
	if runErr != nil {
		_, err := s.db.ExecContext(ctx, `INSERT INTO site_runs (source_id, ran_at, error, headlines) VALUES (?, ?, ?, 0)
			ON CONFLICT(source_id) DO UPDATE SET ran_at = excluded.ran_at, error = excluded.error, headlines = 0`,
			sourceID, millis(now), runErr.Error())
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_runs (source_id, ran_at, ok_at, error, headlines) VALUES (?, ?, ?, NULL, ?)
		ON CONFLICT(source_id) DO UPDATE SET ran_at = excluded.ran_at, ok_at = excluded.ok_at, error = NULL,
			headlines = excluded.headlines`, sourceID, millis(now), millis(now), headlines)
	return err
}

// SiteRuns are every site's last run, by source id.
func (s *Store) SiteRuns(ctx context.Context) (map[string]SiteRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, ran_at, ok_at, error, headlines FROM site_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SiteRun{}
	for rows.Next() {
		var r SiteRun
		var ran int64
		var ok sql.NullInt64
		var runErr sql.NullString
		if err := rows.Scan(&r.SourceID, &ran, &ok, &runErr, &r.Headlines); err != nil {
			return nil, err
		}
		r.RanAt = time.UnixMilli(ran).UTC()
		if ok.Valid {
			r.OKAt = time.UnixMilli(ok.Int64).UTC()
		}
		r.Error = runErr.String
		out[r.SourceID] = r
	}
	return out, rows.Err()
}

// Prune deletes the headlines last seen before before, with their articles and failures. It returns how many
// headlines went.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff := millis(before)
	for _, q := range []string{
		`DELETE FROM articles WHERE url IN (SELECT url FROM headlines WHERE last_seen < ?)`,
		`DELETE FROM failures WHERE url IN (SELECT url FROM headlines WHERE last_seen < ?)`,
	} {
		if _, err := tx.ExecContext(ctx, q, cutoff); err != nil {
			return 0, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM headlines WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, tx.Commit()
}

// Vectors are the headline vectors saved in space (a model and a story text), by key.
func (s *Store) Vectors(ctx context.Context, space string) (map[string][]float32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, vector FROM story_vectors WHERE space = ?`, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	for rows.Next() {
		var key string
		var blob []byte
		if err := rows.Scan(&key, &blob); err != nil {
			return nil, err
		}
		v := make([]float32, len(blob)/4)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		out[key] = v
	}
	return out, rows.Err()
}

// SaveVectors saves added and deletes removed in space, in one transaction.
func (s *Store) SaveVectors(ctx context.Context, space string, added map[string][]float32, removed []string) error {
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range added {
		blob := make([]byte, len(v)*4)
		for i, x := range v {
			binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(x))
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO story_vectors (space, key, vector) VALUES (?, ?, ?)`, space, key, blob); err != nil {
			return err
		}
	}
	for _, key := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM story_vectors WHERE space = ? AND key = ?`, space, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}
