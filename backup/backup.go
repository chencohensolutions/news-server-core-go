// Package backup keeps everything the server can't read again in plain files beside the database: the headlines and
// articles (with the pages they were read from), every version of every generated story, every digest, the threads,
// and the settings the app changed. A server installed from scratch gets it all back with `news-server restore`.
//
// The folder (archive_dir, "archive" by default):
//
//	state.json                          how far each table is archived
//	headlines/2026-11/2026-11-11.jsonl  one row a line, by the day it was first seen (UTC)
//	articles/2026-11/2026-11-11.jsonl.gz  with the page each was read from; gzip, a member each archiving
//	story_records/…  summaries/…  digests/…  unrelated/…  gemini_groups/…  the same, by the day written:
//	                                    a row written again is added again, so every version is kept
//	threads.json  story_threads.json  thread_links.json  server_settings.json  site_settings.json
//	gemini_requests.json  gemini_tokens.json      the whole table, written again each time
//
// Not kept: the vectors (the model makes them again), the sites' latest runs and failed fetches. The certificates,
// devices.json, config.json and gemini.key are files already: copy them with the archive.
package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/store"
)

// added are the tables whose rows are added to day files, by a column of Unix milliseconds.
var added = []struct{ table, by string }{
	{"headlines", "first_seen"},
	{"articles", "fetched_at"},
	{"summaries", "written_at"},
	{"story_records", "written_at"},
	{"digests", "written_at"},
	{"unrelated", "flagged_at"},
	{"gemini_groups", "placed_at"},
}

// whole are the small tables written whole each time.
var whole = []string{"threads", "story_threads", "thread_links", "server_settings", "site_settings", "gemini_requests", "gemini_tokens"}

const (
	batch = 2000
	// settle is how old a row must be to be archived: a row written in the same millisecond as the last one archived
	// would otherwise be missed.
	settle = 2 * time.Second
)

// state is how far each added table is archived: its last row's milliseconds.
type state map[string]int64

// Result is what one Archive added.
type Result struct {
	Rows map[string]int // by table
}

func (r Result) String() string {
	var parts []string
	for _, t := range added {
		if n := r.Rows[t.table]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, t.table))
		}
	}
	if len(parts) == 0 {
		return "nothing new"
	}
	return strings.Join(parts, ", ")
}

// Archive adds what db has that dir doesn't yet, and writes the small tables again.
func Archive(ctx context.Context, db *store.Store, dir string, now time.Time) (Result, error) {
	result := Result{Rows: map[string]int{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, err
	}
	done, err := readState(dir)
	if err != nil {
		return result, err
	}
	upTo := now.Add(-settle).UnixMilli()
	for _, t := range added {
		for {
			files := map[string]*bytes.Buffer{}
			last := done[t.table]
			n, err := db.Rows(ctx, t.table, t.by, last, upTo, batch, func(row store.Row) error {
				ms, _ := row[t.by].(int64)
				name := dayFile(t.table, ms)
				if files[name] == nil {
					files[name] = &bytes.Buffer{}
				}
				line, err := json.Marshal(row)
				if err != nil {
					return err
				}
				files[name].Write(append(line, '\n'))
				last = max(last, ms)
				return nil
			})
			if err != nil {
				return result, fmt.Errorf("archiving %s: %w", t.table, err)
			}
			for name, data := range files {
				if err := appendTo(filepath.Join(dir, name), data.Bytes()); err != nil {
					return result, err
				}
			}
			result.Rows[t.table] += n
			done[t.table] = last
			// The state after the files: a stop between adds the rows again next time, and restoring replaces them.
			if err := writeState(dir, done); err != nil {
				return result, err
			}
			if n < batch {
				break
			}
		}
	}
	for _, table := range whole {
		var rows []store.Row
		if _, err := db.Rows(ctx, table, "", 0, 0, 1_000_000, func(row store.Row) error {
			rows = append(rows, row)
			return nil
		}); err != nil {
			return result, fmt.Errorf("archiving %s: %w", table, err)
		}
		if rows == nil {
			rows = []store.Row{}
		}
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return result, err
		}
		if err := writeFile(filepath.Join(dir, table+".json"), data); err != nil {
			return result, err
		}
	}
	return result, nil
}

// dayFile is the file of table's rows of the UTC day of ms.
func dayFile(table string, ms int64) string {
	day := time.UnixMilli(ms).UTC().Format("2006-01-02")
	name := filepath.Join(table, day[:7], day+".jsonl")
	if table == "articles" {
		name += ".gz"
	}
	return name
}

// appendTo adds data to the file at path; to a .gz file as a gzip member of its own.
func appendTo(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(path, ".gz") {
		var zipped bytes.Buffer
		zw := gzip.NewWriter(&zipped)
		zw.Write(data)
		if err := zw.Close(); err != nil {
			return err
		}
		data = zipped.Bytes()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeFile writes path whole: to a new file first, so a stop never leaves half of it.
func writeFile(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readState(dir string) (state, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := state{}
	return s, json.Unmarshal(data, &s)
}

func writeState(dir string, s state) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "state.json"), data)
}

// Restore puts dir's archive into db (a new one): the small tables, then each added table's day files in order, so
// a row's latest version is the one kept. A line cut short (the server stopped while adding it) is skipped and told.
func Restore(ctx context.Context, db *store.Store, dir string, log func(format string, args ...any)) (Result, error) {
	result := Result{Rows: map[string]int{}}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		return result, fmt.Errorf("%s is not an archive: %w", dir, err)
	}
	for _, table := range whole {
		data, err := os.ReadFile(filepath.Join(dir, table+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return result, fmt.Errorf("%s.json: %w", table, err)
		}
		rows := make([]store.Row, 0, len(raw))
		for _, r := range raw {
			rows = append(rows, rowOf(r))
		}
		if err := db.PutRows(ctx, table, rows); err != nil {
			return result, err
		}
		result.Rows[table] = len(rows)
	}
	for _, t := range added {
		files, err := filepath.Glob(filepath.Join(dir, t.table, "*", "*.jsonl*"))
		if err != nil {
			return result, err
		}
		sort.Strings(files)
		for _, path := range files {
			n, err := restoreFile(ctx, db, t.table, path, log)
			result.Rows[t.table] += n
			if err != nil {
				return result, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	return result, nil
}

func restoreFile(ctx context.Context, db *store.Store, table, path string, log func(string, ...any)) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return 0, err
		}
		r = zr
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 64<<20) // a page can be megabytes
	var rows []store.Row
	n := 0
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		err := db.PutRows(ctx, table, rows)
		n += len(rows)
		rows = rows[:0]
		return err
	}
	for scanner.Scan() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			log("restore: %s: skipped a line cut short", path)
			continue
		}
		rows = append(rows, rowOf(raw))
		if len(rows) == 500 {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// A gzip member cut short ends the file: what came before it is kept.
		log("restore: %s: %v; the rows before it are restored", path, err)
	}
	return n, flush()
}

// rowOf is a row read back: JSON columns as their JSON, numbers as integers, text as text.
func rowOf(raw map[string]json.RawMessage) store.Row {
	row := make(store.Row, len(raw))
	for c, v := range raw {
		v = bytes.TrimSpace(v)
		switch {
		case len(v) == 0 || string(v) == "null":
			row[c] = nil
		case v[0] == '{' || v[0] == '[':
			row[c] = json.RawMessage(v)
		case v[0] == '"':
			var s string
			json.Unmarshal(v, &s)
			row[c] = s
		default:
			var n json.Number
			json.Unmarshal(v, &n)
			row[c] = n
		}
	}
	return row
}
