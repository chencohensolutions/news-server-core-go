package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Row is one table row by column, for the archive: numbers, text, or JSON (the columns that hold JSON).
type Row map[string]any

// jsonColumns are the columns that hold JSON: the archive writes them as JSON, not as a string of it.
var jsonColumns = map[string]bool{"item": true, "article": true, "summary": true, "digest": true, "value": true}

// tables are the tables Rows and PutRows take: no other name reaches the SQL.
var tables = map[string]bool{
	"headlines": true, "articles": true, "summaries": true, "digests": true, "story_records": true, "unrelated": true,
	"gemini_groups": true, "threads": true, "story_threads": true, "thread_links": true, "server_settings": true,
	"site_settings": true, "gemini_requests": true, "gemini_tokens": true,
}

// Rows calls fn with table's rows, in the order of by (a column of Unix milliseconds) from after (exclusive) to upTo
// (inclusive), at most limit; by "" is every row (limit too). It returns how many it read.
func (s *Store) Rows(ctx context.Context, table, by string, after, upTo int64, limit int, fn func(Row) error) (int, error) {
	if !tables[table] || strings.ContainsAny(by, " ;'\"") {
		return 0, fmt.Errorf("store: no table %q to archive", table)
	}
	query := `SELECT * FROM ` + table
	var args []any
	if by != "" {
		query += ` WHERE ` + by + ` > ? AND ` + by + ` <= ? ORDER BY ` + by
		args = append(args, after, upTo)
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return n, err
		}
		row := make(Row, len(columns))
		for i, c := range columns {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if text, ok := v.(string); ok && jsonColumns[c] && json.Valid([]byte(text)) {
				v = json.RawMessage(text)
			}
			row[c] = v
		}
		if err := fn(row); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// PutRows puts rows into table, each replacing the row with its key: what Rows read, back.
func (s *Store) PutRows(ctx context.Context, table string, rows []Row) error {
	if !tables[table] {
		return fmt.Errorf("store: no table %q to restore", table)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range rows {
		columns := make([]string, 0, len(row))
		marks := make([]string, 0, len(row))
		values := make([]any, 0, len(row))
		for c, v := range row {
			if strings.ContainsAny(c, " ;'\"()") {
				return fmt.Errorf("store: bad column %q", c)
			}
			switch v := v.(type) {
			case json.RawMessage:
				values = append(values, string(v))
			case json.Number:
				n, err := v.Int64()
				if err != nil {
					values = append(values, v.String())
				} else {
					values = append(values, n)
				}
			case map[string]any, []any:
				data, _ := json.Marshal(v)
				values = append(values, string(data))
			default:
				values = append(values, v)
			}
			columns = append(columns, c)
			marks = append(marks, "?")
		}
		query := `INSERT OR REPLACE INTO ` + table + ` (` + strings.Join(columns, ", ") + `) VALUES (` + strings.Join(marks, ", ") + `)`
		if _, err := tx.ExecContext(ctx, query, values...); err != nil {
			return fmt.Errorf("store: %s: %w", table, err)
		}
	}
	return tx.Commit()
}
