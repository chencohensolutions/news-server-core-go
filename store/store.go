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
	"strings"
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
CREATE TABLE IF NOT EXISTS summaries (
	key TEXT PRIMARY KEY,        -- the story's report URLs, hashed (summary.StoryKey)
	summary TEXT NOT NULL,       -- what Gemini wrote, as JSON
	written_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS digests (
	id INTEGER PRIMARY KEY,      -- only 1: the latest
	digest TEXT NOT NULL,
	written_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gemini_requests (
	day TEXT PRIMARY KEY,        -- the free tier's day, in Pacific time
	requests INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS gemini_tokens (
	day TEXT PRIMARY KEY,        -- as gemini_requests
	tokens_in INTEGER NOT NULL,      -- the prompts, cached ones included
	tokens_cached INTEGER NOT NULL,  -- of tokens_in, read from Gemini's cache
	tokens_out INTEGER NOT NULL,     -- the answers
	tokens_thoughts INTEGER NOT NULL -- the model's thinking, billed as output
);
CREATE TABLE IF NOT EXISTS unrelated (
	url TEXT NOT NULL,           -- a headline Gemini found to be about another event than its story
	other_url TEXT NOT NULL,     -- one of that story's other reports
	flagged_at INTEGER NOT NULL,
	PRIMARY KEY (url, other_url)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS story_records (
	id TEXT PRIMARY KEY,         -- a story's id, kept by its later versions (summary.StorySummary.ID)
	summary TEXT NOT NULL,       -- its latest version, as JSON
	first_written INTEGER NOT NULL,
	written_at INTEGER NOT NULL,
	linked INTEGER NOT NULL DEFAULT 0 -- 1 once Gemini placed it in its threads, or none
);
CREATE INDEX IF NOT EXISTS story_records_linked ON story_records (linked, first_written);
CREATE TABLE IF NOT EXISTS story_threads (
	story TEXT NOT NULL,         -- a story in a thread: a story can be in several, a thread has many
	thread TEXT NOT NULL,
	PRIMARY KEY (story, thread)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS story_threads_thread ON story_threads (thread);
CREATE TABLE IF NOT EXISTS threads (
	id TEXT PRIMARY KEY,         -- an affair that runs over months: stories Gemini linked
	name TEXT NOT NULL,
	summary TEXT NOT NULL,       -- the story so far
	started_at INTEGER NOT NULL, -- its first story's
	updated_at INTEGER NOT NULL  -- its latest story's
);
CREATE TABLE IF NOT EXISTS thread_links (
	a TEXT NOT NULL,             -- two related threads (a war and the strike during it), each way
	b TEXT NOT NULL,
	PRIMARY KEY (a, b)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS server_settings (
	key TEXT PRIMARY KEY,        -- a setting the app changes on the server ("grouping")
	value TEXT NOT NULL          -- as JSON
);
CREATE TABLE IF NOT EXISTS site_settings (
	source_id TEXT PRIMARY KEY,  -- what the app's Sources screen chose for one site
	enabled INTEGER NOT NULL,
	feed INTEGER NOT NULL,
	wait_ms INTEGER NOT NULL
);
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

// RecentDays is how far back what reads "every headline" goes, now that they are kept for good: a week, the most
// stories, summaries and a first sync need.
const RecentDays = 7

// Recent is since for RecentDays before now, or days before now when that is longer.
func Recent(now time.Time, days int) time.Time {
	return now.AddDate(0, 0, -max(RecentDays, days))
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

// SiteSetting is what the app's Sources screen chose for one site: whether the server reads it, from its RSS feed
// instead of its pages, and its wait between two requests in milliseconds.
type SiteSetting struct {
	Enabled bool
	Feed    bool
	WaitMs  int
}

// SiteSettings are the sites' saved settings, by source id; a site never set has none.
func (s *Store) SiteSettings(ctx context.Context) (map[string]SiteSetting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, enabled, feed, wait_ms FROM site_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SiteSetting{}
	for rows.Next() {
		var id string
		var set SiteSetting
		if err := rows.Scan(&id, &set.Enabled, &set.Feed, &set.WaitMs); err != nil {
			return nil, err
		}
		out[id] = set
	}
	return out, rows.Err()
}

// SaveSiteSetting keeps sourceID's settings.
func (s *Store) SaveSiteSetting(ctx context.Context, sourceID string, set SiteSetting) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_settings (source_id, enabled, feed, wait_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET enabled = excluded.enabled, feed = excluded.feed, wait_ms = excluded.wait_ms`,
		sourceID, set.Enabled, set.Feed, set.WaitMs)
	return err
}

// Summaries are the stored summaries of keys that have one, as JSON, by key.
func (s *Store) Summaries(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, summary FROM summaries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var key, data string
		if err := rows.Scan(&key, &data); err != nil {
			return nil, err
		}
		out[key] = json.RawMessage(data)
	}
	return out, rows.Err()
}

// SaveSummary keeps key's summary, written at now.
func (s *Store) SaveSummary(ctx context.Context, key string, summary json.RawMessage, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO summaries (key, summary, written_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET summary = excluded.summary, written_at = excluded.written_at`,
		key, string(summary), millis(now))
	return err
}

// PruneSummaries deletes the summaries written before before whose key isn't in keep.
func (s *Store) PruneSummaries(ctx context.Context, keep map[string]bool, before time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM summaries WHERE written_at < ?`, millis(before))
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		if !keep[key] {
			gone = append(gone, key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range gone {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM summaries WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}

// Digest is the latest digest as JSON, or nil.
func (s *Store) Digest(ctx context.Context) (json.RawMessage, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT digest FROM digests WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// SaveDigest keeps digest as the latest, written at now.
func (s *Store) SaveDigest(ctx context.Context, digest json.RawMessage, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO digests (id, digest, written_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET digest = excluded.digest, written_at = excluded.written_at`, string(digest), millis(now))
	return err
}

// GeminiRequests is how many Gemini requests were sent on day.
func (s *Store) GeminiRequests(ctx context.Context, day string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT requests FROM gemini_requests WHERE day = ?`, day).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// CountGeminiRequest adds one request to day.
func (s *Store) CountGeminiRequest(ctx context.Context, day string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO gemini_requests (day, requests) VALUES (?, 1)
		ON CONFLICT(day) DO UPDATE SET requests = requests + 1`, day)
	return err
}

// Tokens are Gemini tokens: the prompts (Cached of them read from its cache), the answers, and the thinking.
type Tokens struct{ In, Cached, Out, Thoughts int }

// CountGeminiTokens adds one answer's tokens to day.
func (s *Store) CountGeminiTokens(ctx context.Context, day string, t Tokens) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO gemini_tokens (day, tokens_in, tokens_cached, tokens_out, tokens_thoughts)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(day) DO UPDATE SET tokens_in = tokens_in + excluded.tokens_in,
		tokens_cached = tokens_cached + excluded.tokens_cached, tokens_out = tokens_out + excluded.tokens_out,
		tokens_thoughts = tokens_thoughts + excluded.tokens_thoughts`, day, t.In, t.Cached, t.Out, t.Thoughts)
	return err
}

// GeminiDay is one day's Gemini use.
type GeminiDay struct {
	Day      string
	Requests int
	Tokens
}

// GeminiDays are the last n days with Gemini use, newest first.
func (s *Store) GeminiDays(ctx context.Context, n int) ([]GeminiDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.day, r.requests, COALESCE(t.tokens_in, 0), COALESCE(t.tokens_cached, 0),
		COALESCE(t.tokens_out, 0), COALESCE(t.tokens_thoughts, 0)
		FROM gemini_requests r LEFT JOIN gemini_tokens t ON t.day = r.day ORDER BY r.day DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GeminiDay
	for rows.Next() {
		var d GeminiDay
		if err := rows.Scan(&d.Day, &d.Requests, &d.In, &d.Cached, &d.Out, &d.Thoughts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Counts are how many headlines, articles and story summaries are stored.
type Counts struct{ Headlines, Articles, Summaries int }

// Counts counts what is stored.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM headlines), (SELECT COUNT(*) FROM articles),
		(SELECT COUNT(*) FROM summaries)`).Scan(&c.Headlines, &c.Articles, &c.Summaries)
	return c, err
}

// SaveUnrelated keeps that url was found to be about another event than others, the rest of its story.
func (s *Store) SaveUnrelated(ctx context.Context, url string, others []string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, other := range others {
		if _, err := tx.ExecContext(ctx, `INSERT INTO unrelated (url, other_url, flagged_at) VALUES (?, ?, ?)
			ON CONFLICT(url, other_url) DO UPDATE SET flagged_at = excluded.flagged_at`, url, other, millis(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Unrelated is what SaveUnrelated kept since since: each flagged URL and the URLs it is not about the same event as.
// Older flags are deleted.
func (s *Store) Unrelated(ctx context.Context, since time.Time) (map[string]map[string]bool, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM unrelated WHERE flagged_at < ?`, millis(since)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT url, other_url FROM unrelated`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var url, other string
		if err := rows.Scan(&url, &other); err != nil {
			return nil, err
		}
		if out[url] == nil {
			out[url] = map[string]bool{}
		}
		out[url][other] = true
	}
	return out, rows.Err()
}

// ServerSetting is the setting key the app saved; false when none is.
func (s *Store) ServerSetting(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM server_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(value), true, nil
}

// SaveServerSetting keeps key's value.
func (s *Store) SaveServerSetting(ctx context.Context, key string, value json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(value))
	return err
}

// StoryRecord is a story kept for good: its latest version, when its first and latest were written, and whether
// Gemini placed it in its threads yet.
type StoryRecord struct {
	ID           string
	Summary      json.RawMessage
	FirstWritten time.Time
	WrittenAt    time.Time
	Linked       bool
}

// SaveStoryRecord keeps id's latest version, written at now; its first time and thread stay.
func (s *Store) SaveStoryRecord(ctx context.Context, id string, summary json.RawMessage, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO story_records (id, summary, first_written, written_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET summary = excluded.summary, written_at = excluded.written_at`,
		id, string(summary), millis(now), millis(now))
	return err
}

const recordColumns = `id, summary, first_written, written_at, linked`

func scanRecords(rows *sql.Rows) ([]StoryRecord, error) {
	defer rows.Close()
	var out []StoryRecord
	for rows.Next() {
		var r StoryRecord
		var data string
		var first, written int64
		if err := rows.Scan(&r.ID, &data, &first, &written, &r.Linked); err != nil {
			return nil, err
		}
		r.Summary = json.RawMessage(data)
		r.FirstWritten, r.WrittenAt = time.UnixMilli(first).UTC(), time.UnixMilli(written).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// StoryRecord is id's record, or nil.
func (s *Store) StoryRecord(ctx context.Context, id string) (*StoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM story_records WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	out, err := scanRecords(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// UnlinkedStories are up to limit stories not yet placed in a thread, oldest first.
func (s *Store) UnlinkedStories(ctx context.Context, limit int) ([]StoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM story_records WHERE linked = 0
		ORDER BY first_written, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// ThreadStories are thread's stories, oldest first.
func (s *Store) ThreadStories(ctx context.Context, thread string) ([]StoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM story_records
		WHERE id IN (SELECT story FROM story_threads WHERE thread = ?) ORDER BY first_written, id`, thread)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// LinkStory puts story id in threads (none: in no thread): it is placed, and not offered again.
func (s *Store) LinkStory(ctx context.Context, id string, threads []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range threads {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO story_threads (story, thread) VALUES (?, ?)`, id, t); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE story_records SET linked = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// StoryThreads are the threads story id is in, latest updated first.
func (s *Store) StoryThreads(ctx context.Context, id string) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, threadQuery+` WHERE t.id IN (SELECT thread FROM story_threads WHERE story = ?)
		ORDER BY t.updated_at DESC, t.id`, id)
	if err != nil {
		return nil, err
	}
	return scanThreads(rows)
}

// Thread is an affair over months: Gemini's name for it, the story so far, and its stories' span and count.
type Thread struct {
	ID        string
	Name      string
	Summary   string
	StartedAt time.Time
	UpdatedAt time.Time
	Stories   int
}

const threadQuery = `SELECT t.id, t.name, t.summary, t.started_at, t.updated_at,
	(SELECT COUNT(*) FROM story_threads st WHERE st.thread = t.id) FROM threads t`

func scanThreads(rows *sql.Rows) ([]Thread, error) {
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var t Thread
		var started, updated int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Summary, &started, &updated, &t.Stories); err != nil {
			return nil, err
		}
		t.StartedAt, t.UpdatedAt = time.UnixMilli(started).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// Threads are up to limit threads, latest updated first.
func (s *Store) Threads(ctx context.Context, limit int) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, threadQuery+` ORDER BY t.updated_at DESC, t.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanThreads(rows)
}

// ThreadByID is thread id, or nil.
func (s *Store) ThreadByID(ctx context.Context, id string) (*Thread, error) {
	rows, err := s.db.QueryContext(ctx, threadQuery+` WHERE t.id = ?`, id)
	if err != nil {
		return nil, err
	}
	out, err := scanThreads(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// SaveThread keeps t (its Stories aside).
func (s *Store) SaveThread(ctx context.Context, t Thread) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO threads (id, name, summary, started_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, summary = excluded.summary, started_at = excluded.started_at,
		updated_at = excluded.updated_at`, t.ID, t.Name, t.Summary, millis(t.StartedAt), millis(t.UpdatedAt))
	return err
}

// HeadlinesByURL are the stored headlines of urls, by URL; ones not stored are left out.
func (s *Store) HeadlinesByURL(ctx context.Context, urls []string) (map[string]Headline, error) {
	out := map[string]Headline{}
	for start := 0; start < len(urls); start += 500 {
		chunk := urls[start:min(len(urls), start+500)]
		args := make([]any, len(chunk))
		for i, u := range chunk {
			args[i] = u
		}
		rows, err := s.db.QueryContext(ctx, `SELECT item, first_seen, last_seen FROM headlines WHERE url IN (?`+
			strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var data string
			var first, last int64
			if err := rows.Scan(&data, &first, &last); err != nil {
				rows.Close()
				return nil, err
			}
			var h Headline
			if err := json.Unmarshal([]byte(data), &h.ArticleSummary); err != nil {
				rows.Close()
				return nil, err
			}
			h.FirstSeen, h.LastSeen = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
			out[h.URL] = h
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RelateThreads links threads a and b, both ways.
func (s *Store) RelateThreads(ctx context.Context, a, b string) error {
	if a == b {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO thread_links (a, b) VALUES (?, ?), (?, ?)`, a, b, b, a)
	return err
}

// RelatedThreads are the threads related to id, latest updated first.
func (s *Store) RelatedThreads(ctx context.Context, id string) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, threadQuery+` WHERE t.id IN (SELECT b FROM thread_links WHERE a = ?)
		ORDER BY t.updated_at DESC, t.id`, id)
	if err != nil {
		return nil, err
	}
	return scanThreads(rows)
}
