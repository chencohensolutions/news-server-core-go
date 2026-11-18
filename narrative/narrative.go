// Package narrative keeps the meaning of the stories and threads for good, so a new story finds the earlier stories
// and the threads it continues anywhere in the archive: one vector per story (its title and first sentences, by the
// story model) and one per thread (its stories' vectors, the recent ones weighing more). The thread linking shows
// Gemini only the threads near the new stories, and the earlier stories near them, in place of every thread.
package narrative

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/store"
)

// HalfLife is how fast a story's weight in its thread's vector fades: one HalfLife older than the thread's latest
// story, half the weight. A thread that changed course points where it is now, not where it began.
const HalfLife = 30 * 24 * time.Hour

// storySentences is how many of a story's first sentences are embedded with its title (the model reads 128
// tokens: about the title and two sentences).
const storySentences = 2

// Embedder turns a text into a unit vector: the story model.
type Embedder interface {
	Embed(text string) ([]float32, error)
}

// Index is the narrative vectors of one model, in the store.
type Index struct {
	Store    *store.Store
	Embedder Embedder
	// Space names the model and the text: "bge-m3/narrative".
	Space string
}

// New is the index of model's vectors in db.
func New(db *store.Store, embedder Embedder, model string) *Index {
	return &Index{Store: db, Embedder: embedder, Space: model + "/narrative"}
}

// Story is what a story's vector is made from.
type Story struct {
	ID        string
	At        time.Time // its first time
	Title     string
	Sentences []string
}

// Text is what is embedded for s: its title and first sentences.
func (s Story) Text() string {
	parts := []string{strings.TrimSpace(s.Title)}
	for _, sentence := range s.Sentences[:min(len(s.Sentences), storySentences)] {
		if t := strings.TrimSpace(sentence); t != "" {
			parts = append(parts, t)
		}
	}
	text := parts[0]
	for _, p := range parts[1:] {
		if text != "" && !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "!") {
			text += "."
		}
		text += " " + p
	}
	return strings.TrimSpace(text)
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// EmbedStories keeps a vector for each of stories not embedded yet, or whose text changed since; it returns how many
// it embedded. Stopped by ctx, what is embedded is kept.
func (ix *Index) EmbedStories(ctx context.Context, stories []Story) (int, error) {
	known, err := ix.Store.NarrativeHashes(ctx, ix.Space, store.KindStory)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range stories {
		text := s.Text()
		if text == "" || known[s.ID] == hash(text) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return n, err
		}
		v, err := ix.Embedder.Embed(text)
		if err != nil {
			return n, fmt.Errorf("narrative: embedding story %s: %w", s.ID, err)
		}
		if err := ix.Store.SaveNarrative(ctx, store.Narrative{Space: ix.Space, Kind: store.KindStory, Ref: s.ID, At: s.At,
			Hash: hash(text), Vector: v}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Member is one of a thread's stories: its vector and first time.
type Member struct {
	At     time.Time
	Vector []float32
}

// ThreadVector is the mean of members' vectors, each weighted by its age at at (HalfLife), as a unit vector; nil
// without members.
func ThreadVector(members []Member, at time.Time, halfLife time.Duration) []float32 {
	var sum []float32
	for _, m := range members {
		if len(m.Vector) == 0 {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(m.Vector))
		}
		if len(m.Vector) != len(sum) {
			continue
		}
		w := float32(1)
		if halfLife > 0 {
			if age := at.Sub(m.At); age > 0 {
				w = float32(math.Exp2(-float64(age) / float64(halfLife)))
			}
		}
		for i, x := range m.Vector {
			sum[i] += w * x
		}
	}
	return Normalize(sum)
}

// Normalize scales v to unit length in place, and returns it; nil for a zero vector.
func Normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return nil
	}
	scale := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= scale
	}
	return v
}

// Dot is the similarity of two unit vectors.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range min(len(a), len(b)) {
		s += a[i] * b[i]
	}
	return s
}

// UpdateThreads keeps the vector of each of threads (by id) whose stories changed since it was made, from its
// stories' vectors; members are each thread's story ids. It returns how many it made.
func (ix *Index) UpdateThreads(ctx context.Context, threads []store.Thread, members map[string][]string) (int, error) {
	known, err := ix.Store.NarrativeHashes(ctx, ix.Space, store.KindThread)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range threads {
		ids := members[t.ID]
		h := fmt.Sprintf("%d:%d", len(ids), t.UpdatedAt.UnixMilli())
		if len(ids) == 0 || known[t.ID] == h {
			continue
		}
		vectors, err := ix.Store.NarrativeVectors(ctx, ix.Space, store.KindStory, ids)
		if err != nil {
			return n, err
		}
		var list []Member
		for _, id := range ids {
			if v, ok := vectors[id]; ok {
				list = append(list, Member{At: v.At, Vector: v.Vector})
			}
		}
		v := ThreadVector(list, t.UpdatedAt, HalfLife)
		if v == nil {
			continue
		}
		if err := ix.Store.SaveNarrative(ctx, store.Narrative{Space: ix.Space, Kind: store.KindThread, Ref: t.ID,
			At: t.UpdatedAt, Hash: h, Vector: v}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Vector is the kind's id's kept vector, or nil.
func (ix *Index) Vector(ctx context.Context, kind, id string) ([]float32, error) {
	got, err := ix.Store.NarrativeVectors(ctx, ix.Space, kind, []string{id})
	if err != nil {
		return nil, err
	}
	return got[id].Vector, nil
}

// Nearest are the k narratives of the kind closest to v, closest first; before, when not zero, keeps the ones
// earlier than it.
func (ix *Index) Nearest(ctx context.Context, kind string, v []float32, k int, before time.Time) ([]store.Neighbor, error) {
	return ix.Store.NearestNarratives(ctx, ix.Space, kind, v, k, before)
}
