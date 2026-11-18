package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	// sqlite-vec, built into modernc's SQLite: the vec0 tables the narrative vectors are searched in.
	_ "modernc.org/sqlite/vec"
)

// Narrative vectors are the meaning of the stories and threads, kept for good, so a new story finds the earlier ones
// and the threads it continues across the whole archive (the headline vectors only last the grouping's days). The
// rows are in narratives; their vectors in a sqlite-vec table per vector size (narrative_vec_1024 for bge-m3), whose
// rowid is the narratives row's. The archive doesn't keep them: `news-server narratives` embeds them again.

// Narrative is one story's or thread's vector in a space (a model and what was embedded).
type Narrative struct {
	Space string
	Kind  string // KindStory or KindThread
	Ref   string // the story's or thread's id
	// At is the story's first time, or the thread's latest story's.
	At time.Time
	// Hash is what the vector was made from, so a changed story or thread is embedded again.
	Hash   string
	Vector []float32
}

const (
	KindStory  = "story"
	KindThread = "thread"
)

// Neighbor is one narrative near a vector: Similarity is the cosine, 1 the same.
type Neighbor struct {
	Ref        string
	At         time.Time
	Similarity float32
}

func vecTable(dims int) string { return fmt.Sprintf("narrative_vec_%d", dims) }

func (s *Store) ensureVecTable(ctx context.Context, dims int) (string, error) {
	if dims <= 0 || dims > 8192 {
		return "", fmt.Errorf("store: a vector of %d dims", dims)
	}
	name := vecTable(dims)
	if _, done := s.vecTables.Load(dims); done {
		return name, nil
	}
	_, err := s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS `+name+` USING vec0(
		space TEXT PARTITION KEY, kind TEXT, at INTEGER, embedding float[`+fmt.Sprint(dims)+`] distance_metric=cosine)`)
	if err != nil {
		return "", fmt.Errorf("store: the vector table: %w", err)
	}
	s.vecTables.Store(dims, true)
	return name, nil
}

func vectorBlob(v []float32) []byte {
	blob := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(x))
	}
	return blob
}

func blobVector(blob []byte) []float32 {
	v := make([]float32, len(blob)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return v
}

// SaveNarrative keeps n's vector, replacing the one it had.
func (s *Store) SaveNarrative(ctx context.Context, n Narrative) error {
	table, err := s.ensureVecTable(ctx, len(n.Vector))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rowid int64
	var dims int
	err = tx.QueryRowContext(ctx, `SELECT rowid, dims FROM narratives WHERE space = ? AND kind = ? AND ref = ?`, n.Space, n.Kind, n.Ref).Scan(&rowid, &dims)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `INSERT INTO narratives (space, kind, ref, at, hash, dims) VALUES (?, ?, ?, ?, ?, ?)`,
			n.Space, n.Kind, n.Ref, millis(n.At), n.Hash, len(n.Vector))
		if err != nil {
			return err
		}
		if rowid, err = res.LastInsertId(); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+vecTable(dims)+` WHERE rowid = ?`, rowid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE narratives SET at = ?, hash = ?, dims = ? WHERE rowid = ?`,
			millis(n.At), n.Hash, len(n.Vector), rowid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (rowid, space, kind, at, embedding) VALUES (?, ?, ?, ?, ?)`,
		rowid, n.Space, n.Kind, millis(n.At), vectorBlob(n.Vector)); err != nil {
		return err
	}
	return tx.Commit()
}

// NarrativeHashes are the hashes of the kind's vectors kept in space, by ref.
func (s *Store) NarrativeHashes(ctx context.Context, space, kind string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ref, hash FROM narratives WHERE space = ? AND kind = ?`, space, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ref, hash string
		if err := rows.Scan(&ref, &hash); err != nil {
			return nil, err
		}
		out[ref] = hash
	}
	return out, rows.Err()
}

// NarrativeVectors are the kept vectors of refs (the kind's, in space), by ref; ones not kept are left out.
func (s *Store) NarrativeVectors(ctx context.Context, space, kind string, refs []string) (map[string]Narrative, error) {
	out := map[string]Narrative{}
	for _, ref := range refs {
		n := Narrative{Space: space, Kind: kind, Ref: ref}
		var rowid, at int64
		var dims int
		err := s.db.QueryRowContext(ctx, `SELECT rowid, at, hash, dims FROM narratives WHERE space = ? AND kind = ? AND ref = ?`,
			space, kind, ref).Scan(&rowid, &at, &n.Hash, &dims)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var blob []byte
		err = s.db.QueryRowContext(ctx, `SELECT embedding FROM `+vecTable(dims)+` WHERE rowid = ?`, rowid).Scan(&blob)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		n.At, n.Vector = time.UnixMilli(at).UTC(), blobVector(blob)
		out[ref] = n
	}
	return out, nil
}

// Narratives calls fn with every vector of the kind in space, oldest first.
func (s *Store) Narratives(ctx context.Context, space, kind string, fn func(Narrative) error) error {
	var dims []int
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT dims FROM narratives WHERE space = ? AND kind = ?`, space, kind)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		dims = append(dims, d)
	}
	rows.Close()
	for _, d := range dims {
		rows, err := s.db.QueryContext(ctx, `SELECT n.ref, n.at, n.hash, v.embedding FROM narratives n JOIN `+vecTable(d)+` v
			ON v.rowid = n.rowid WHERE n.space = ? AND n.kind = ? AND n.dims = ? ORDER BY n.at, n.ref`, space, kind, d)
		if err != nil {
			return err
		}
		for rows.Next() {
			n := Narrative{Space: space, Kind: kind}
			var at int64
			var blob []byte
			if err := rows.Scan(&n.Ref, &at, &n.Hash, &blob); err != nil {
				rows.Close()
				return err
			}
			n.At, n.Vector = time.UnixMilli(at).UTC(), blobVector(blob)
			if err := fn(n); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// NearestNarratives are the k vectors of the kind in space closest to v, closest first; before, when not zero, keeps
// only the ones whose At is earlier.
func (s *Store) NearestNarratives(ctx context.Context, space, kind string, v []float32, k int, before time.Time) ([]Neighbor, error) {
	if k <= 0 || len(v) == 0 {
		return nil, nil
	}
	table, err := s.ensureVecTable(ctx, len(v))
	if err != nil {
		return nil, err
	}
	query := `SELECT n.ref, n.at, v.distance FROM ` + table + ` v JOIN narratives n ON n.rowid = v.rowid
		WHERE v.embedding MATCH ? AND v.k = ? AND v.space = ? AND v.kind = ?`
	args := []any{vectorBlob(v), min(k, 4096), space, kind}
	if !before.IsZero() {
		query += ` AND v.at < ?`
		args = append(args, millis(before))
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY v.distance`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		var at int64
		var distance float64
		if err := rows.Scan(&n.Ref, &at, &distance); err != nil {
			return nil, err
		}
		n.At, n.Similarity = time.UnixMilli(at).UTC(), float32(1-distance)
		out = append(out, n)
	}
	return out, rows.Err()
}

// StoryRecords calls fn with every kept story, oldest first.
func (s *Store) StoryRecords(ctx context.Context, fn func(StoryRecord) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM story_records ORDER BY first_written, id`)
	if err != nil {
		return err
	}
	records, err := scanRecords(rows)
	if err != nil {
		return err
	}
	for _, r := range records {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// StoryRecordsByID are the kept stories of ids, by id; ones not kept are left out.
func (s *Store) StoryRecordsByID(ctx context.Context, ids []string) (map[string]StoryRecord, error) {
	out := map[string]StoryRecord{}
	for _, id := range ids {
		r, err := s.StoryRecord(ctx, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out[id] = *r
		}
	}
	return out, nil
}

// ThreadMembers are every thread's stories, by thread id.
func (s *Store) ThreadMembers(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread, story FROM story_threads ORDER BY thread, story`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var thread, story string
		if err := rows.Scan(&thread, &story); err != nil {
			return nil, err
		}
		out[thread] = append(out[thread], story)
	}
	return out, rows.Err()
}
