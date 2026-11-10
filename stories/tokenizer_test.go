package stories

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The app's UnigramTokenizerTest. ids 0-3 are <s>, <pad>, </s>, <unk> as in XLM-R.
var testTokenizer = func() *Tokenizer {
	t, err := ParseTokenizer([]byte(`{"model": {"type": "Unigram", "unk_id": 3, "vocab": [["<s>", 0.0], ["<pad>", 0.0], ["</s>", 0.0], ["<unk>", 0.0],
		["▁", -2.0], ["▁ש", -4.0], ["▁של", -3.0], ["ום", -3.0], ["▁שלום", -5.0], ["ו", -4.0], ["ם", -4.0], ["\"qא", -1.0]]}}`))
	if err != nil {
		panic(err)
	}
	return t
}()

func ids(v ...int64) []int64 { return v }

func TestTokenizerPicksTheMostLikelySplit(t *testing.T) {
	// "▁שלום" alone (-5) beats "▁של" + "ום" (-6).
	if got := testTokenizer.Encode("שלום", 512); !reflect.DeepEqual(got, ids(0, 8, 2)) {
		t.Fatal(got)
	}
}

func TestUnknownCharactersFuseIntoOneUnk(t *testing.T) {
	if got := testTokenizer.Encode("שלום xyz", 512); !reflect.DeepEqual(got, ids(0, 8, 4, 3, 2)) {
		t.Fatal(got)
	}
}

func TestEscapedPiecesAreDecoded(t *testing.T) {
	if got := testTokenizer.Encode("\"qא", 512); !reflect.DeepEqual(got, ids(0, 4, 11, 2)) {
		t.Fatal(got)
	}
}

func TestTruncationKeepsTheSpecialTokens(t *testing.T) {
	if got := testTokenizer.Encode("שלום שלום שלום", 4); !reflect.DeepEqual(got, ids(0, 8, 8, 2)) {
		t.Fatal(got)
	}
}

// Compares with HuggingFace tokenizers on real headlines. Needs the model files, too big for the repo: set
// E5_TOKENIZER to tokenizer.json and E5_EXPECTED to a JSON list of {text, ids}, as for the app's test.
func TestTokenizerMatchesHuggingFaceOnRealHeadlines(t *testing.T) {
	vocab, expected := os.Getenv("E5_TOKENIZER"), os.Getenv("E5_EXPECTED")
	if vocab == "" || expected == "" {
		t.Skip("E5_TOKENIZER and E5_EXPECTED not set")
	}
	real, err := LoadTokenizer(vocab)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string  `json:"text"`
		IDs  []int64 `json:"ids"`
	}
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
		t.Fatal("no cases", err)
	}
	for _, c := range cases {
		if got := real.Encode(c.Text, 128); !reflect.DeepEqual(got, c.IDs) {
			t.Errorf("%s: %v, want %v", c.Text, got, c.IDs)
		}
	}
}
