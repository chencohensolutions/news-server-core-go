package stories

import (
	"fmt"
	"math"
	"slices"

	"github.com/chencohensolutions/news-server-core-go/onnx"
)

// Pooling is how a text's token vectors become its one vector.
type Pooling int

const (
	PoolCLS  Pooling = iota // the first token's ([CLS]) vector: bge-m3
	PoolMean                // the mean of all of them: e5
)

// Asset is one file of a model: its name, its path upstream, its size and SHA-256.
type Asset struct {
	Name   string
	Path   string
	Bytes  int64
	SHA256 string
}

// ModelSpec is one embedding model stories can be found with (the app's StoryModel): its files, an 8-bit ONNX
// export pinned to a commit and checked by hash, and how it reads a text.
type ModelSpec struct {
	ID        string // the folder its files go to, and the config.json name
	BaseURL   string
	Model     Asset
	Tokenizer Asset
	// Prefix goes before each text: e5 is trained with "query: " for texts compared with each other.
	Prefix  string
	Pooling Pooling
}

// BGEM3 is BAAI/bge-m3 (568M parameters): the more accurate.
var BGEM3 = ModelSpec{
	ID:        "bge-m3",
	BaseURL:   "https://huggingface.co/Xenova/bge-m3/resolve/4de13258303883538bd53b696b452bf8099f0858",
	Model:     Asset{"model_quantized.onnx", "onnx/model_quantized.onnx", 569_694_530, "0826f8c1ab9edf1801db86c61919d4d108e8bfc0b809ec823ad366882ff0b77d"},
	Tokenizer: Asset{"tokenizer.json", "tokenizer.json", 17_082_821, "6710678b12670bc442b99edc952c4d996ae309a7020c1fa0096dd245c2faf790"},
	Pooling:   PoolCLS,
}

// E5Small is intfloat/multilingual-e5-small (118M parameters): about 9 times faster and a fifth of the download,
// a little less accurate.
var E5Small = ModelSpec{
	ID:        "multilingual-e5-small",
	BaseURL:   "https://huggingface.co/Xenova/multilingual-e5-small/resolve/761b726dd34fb83930e26aab4e9ac3899aa1fa78",
	Model:     Asset{"model_quantized.onnx", "onnx/model_quantized.onnx", 118_308_185, "f80102d3f2a1229f387d3c81909990d8945513e347b0eab049f7de3c6f98c193"},
	Tokenizer: Asset{"tokenizer.json", "tokenizer.json", 17_082_730, "0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39"},
	Prefix:    "query: ",
	Pooling:   PoolMean,
}

// Models are the specs by ID.
var Models = map[string]ModelSpec{BGEM3.ID: BGEM3, E5Small.ID: E5Small}

// maxTokens: subtitles average ~60 tokens; 256 measured no different (the app's docs/story-model-comparison.md).
const maxTokens = 128

// Embedder is a model on ONNX Runtime: text in, unit-length vector out (bge-m3: 1,024 dims, e5-small: 384). Safe
// for concurrent use.
type Embedder struct {
	session   *onnx.Session
	tokenizer *Tokenizer
	spec      ModelSpec
	output    string
}

// NewEmbedder runs the model file with up to threads CPU threads.
func NewEmbedder(rt *onnx.Runtime, modelFile string, tokenizer *Tokenizer, spec ModelSpec, threads int) (*Embedder, error) {
	session, err := rt.NewSession(modelFile, threads)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(session.Inputs, "input_ids") || len(session.Outputs) == 0 {
		session.Close()
		return nil, fmt.Errorf("%s takes %v and gives %v: not a text embedding model", modelFile, session.Inputs, session.Outputs)
	}
	return &Embedder{session: session, tokenizer: tokenizer, spec: spec, output: session.Outputs[0]}, nil
}

func (e *Embedder) Close() { e.session.Close() }

// Embed is text's vector, with the model's prefix.
func (e *Embedder) Embed(text string) ([]float32, error) {
	ids := e.tokenizer.Encode(e.spec.Prefix+text, maxTokens)
	shape := []int64{1, int64(len(ids))}
	all := map[string][]int64{"input_ids": ids, "attention_mask": filled(len(ids), 1), "token_type_ids": filled(len(ids), 0)}
	// Only what the exported model declares: bge-m3's export has no token_type_ids.
	inputs := map[string]onnx.Tensor[int64]{}
	for _, name := range e.session.Inputs {
		data, ok := all[name]
		if !ok {
			return nil, fmt.Errorf("the model wants an input %q", name)
		}
		inputs[name] = onnx.Tensor[int64]{Data: data, Shape: shape}
	}
	out, err := onnx.RunInputs(e.session, inputs, e.output)
	if err != nil {
		return nil, err
	}
	hidden := out[0]
	if len(hidden.Shape) != 3 || hidden.Shape[0] != 1 || hidden.Shape[1] != int64(len(ids)) {
		return nil, fmt.Errorf("the model's output has shape %v, not [1 %d dims]", hidden.Shape, len(ids))
	}
	dims := int(hidden.Shape[2])
	tokens := make([][]float32, len(ids))
	for i := range tokens {
		tokens[i] = hidden.Data[i*dims : (i+1)*dims]
	}
	if e.spec.Pooling == PoolCLS {
		return clsNormalized(tokens), nil
	}
	return meanNormalized(tokens), nil
}

func filled(n int, v int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// meanNormalized is e5's embedding: the mean of the tokens' hidden states (all are real tokens: one text, no
// padding), unit length.
func meanNormalized(tokens [][]float32) []float32 {
	v := make([]float32, len(tokens[0]))
	for _, t := range tokens {
		for d := range v {
			v[d] += t[d]
		}
	}
	return unit(v)
}

// clsNormalized is bge-m3's dense embedding: the first ([CLS]) token's hidden state, scaled to unit length.
func clsNormalized(tokens [][]float32) []float32 {
	return unit(slices.Clone(tokens[0]))
}

func unit(v []float32) []float32 {
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	scale := 1 / (float32(math.Sqrt(float64(norm))) + 1e-9)
	for d := range v {
		v[d] *= scale
	}
	return v
}
