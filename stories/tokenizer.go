// Package stories groups headlines from different sites into stories, as the news app does (its
// app/android/.../news/app/stories): each headline's text becomes a vector with an embedding model (bge-m3 or
// multilingual-e5-small, on ONNX Runtime), pairs alike enough are joined in a similarity graph, and the graph is
// cut into stories.
package stories

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer is the XLM-RoBERTa SentencePiece tokenizer bge-m3 and multilingual-e5 use (the app's UnigramTokenizer):
// NFKC, spaces to "▁" with a leading "▁", then a Viterbi split of each "▁" word into the most likely vocabulary
// pieces. It matches HuggingFace tokenizers on the model's tokenizer.json.
type Tokenizer struct {
	ids            map[string]int
	scores         []float32
	unkID          int
	maxPieceLength int
	unkScore       float32
}

const (
	bos          = 0
	eos          = 2
	specialMaxID = 3
	spaceMark    = '▁'
)

var multipleSpaces = regexp.MustCompile(` {2,}`)

// NewTokenizer is a tokenizer over pieces (piece, score) by id, with unkID for characters no piece covers.
func NewTokenizer(pieces []string, scores []float32, unkID int) *Tokenizer {
	t := &Tokenizer{ids: make(map[string]int, len(pieces)), scores: scores, unkID: unkID, maxPieceLength: 1}
	lowest := float32(0)
	for id, piece := range pieces {
		// Special tokens (<s>, <pad>, </s>, <unk>) never match text.
		if _, known := t.ids[piece]; id > specialMaxID && !known {
			t.ids[piece] = id
		}
		t.maxPieceLength = max(t.maxPieceLength, utf8.RuneCountInString(piece))
		if id == 0 || scores[id] < lowest {
			lowest = scores[id]
		}
	}
	t.unkScore = lowest - 10
	return t
}

// LoadTokenizer reads model.vocab and model.unk_id from a HuggingFace tokenizer.json.
func LoadTokenizer(path string) (*Tokenizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseTokenizer(data)
}

// ParseTokenizer reads a tokenizer.json's text.
func ParseTokenizer(data []byte) (*Tokenizer, error) {
	var file struct {
		Model *struct {
			UnkID *int              `json:"unk_id"`
			Vocab []json.RawMessage `json:"vocab"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("tokenizer.json: %w", err)
	}
	if file.Model == nil || file.Model.UnkID == nil || file.Model.Vocab == nil {
		return nil, errors.New("tokenizer.json has no model.unk_id or model.vocab")
	}
	pieces := make([]string, len(file.Model.Vocab))
	scores := make([]float32, len(file.Model.Vocab))
	for i, raw := range file.Model.Vocab {
		var entry [2]any
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("tokenizer.json vocab %d: %w", i, err)
		}
		piece, ok1 := entry[0].(string)
		score, ok2 := entry[1].(float64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("tokenizer.json vocab %d is not [piece, score]", i)
		}
		pieces[i], scores[i] = piece, float32(score)
	}
	return NewTokenizer(pieces, scores, *file.Model.UnkID), nil
}

// Encode is <s>, the pieces, </s>: at most maxTokens ids in all.
func (t *Tokenizer) Encode(text string, maxTokens int) []int64 {
	normalized := multipleSpaces.ReplaceAllString(norm.NFKC.String(text), " ")
	spaced := strings.ReplaceAll(normalized, " ", string(spaceMark))
	if !strings.HasPrefix(spaced, string(spaceMark)) {
		spaced = string(spaceMark) + spaced
	}
	var body []int
	runes := []rune(spaced)
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || runes[i] == spaceMark {
			body = t.encodeWord(runes[start:i], body)
			start = i
		}
	}
	if len(body) > maxTokens-2 {
		body = body[:max(maxTokens-2, 0)]
	}
	out := make([]int64, 0, len(body)+2)
	out = append(out, bos)
	for _, id := range body {
		out = append(out, int64(id))
	}
	return append(out, eos)
}

func (t *Tokenizer) encodeWord(word []rune, out []int) []int {
	n := len(word)
	best := make([]float64, n+1)
	backStart := make([]int, n+1)
	backID := make([]int, n+1)
	for i := range best {
		best[i] = math.Inf(-1)
		backStart[i] = -1
	}
	best[0] = 0
	for end := 1; end <= n; end++ {
		for start := max(0, end-t.maxPieceLength); start < end; start++ {
			if math.IsInf(best[start], -1) {
				continue
			}
			id, ok := t.ids[string(word[start:end])]
			if !ok {
				continue
			}
			if score := best[start] + float64(t.scores[id]); score > best[end] {
				best[end], backStart[end], backID[end] = score, start, id
			}
		}
		// A character no piece covers becomes <unk>, as in HuggingFace's Unigram.
		if _, ok := t.ids[string(word[end-1:end])]; !ok && !math.IsInf(best[end-1], -1) {
			if score := best[end-1] + float64(t.unkScore); score > best[end] {
				best[end], backStart[end], backID[end] = score, end-1, t.unkID
			}
		}
	}
	var reversed []int
	for end := n; end > 0; end = backStart[end] {
		id := backID[end]
		// Consecutive unknown characters fuse into one <unk>.
		if !(id == t.unkID && len(reversed) > 0 && reversed[len(reversed)-1] == t.unkID) {
			reversed = append(reversed, id)
		}
	}
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out
}
