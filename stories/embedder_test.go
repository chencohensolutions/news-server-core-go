package stories

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/chencohensolutions/news-server-core-go/onnx"
)

// The embedder on real ONNX Runtime (set ONNXRUNTIME to its library, or put it where onnx.FindRuntime looks) and
// a tiny model written here: Gather from a table of token vectors, which is what a text model's hidden states are
// for these tests.

// pb is a minimal protobuf writer, enough for an ONNX model.
type pb []byte

func (b pb) varint(field int, v uint64) pb {
	return binary.AppendUvarint(binary.AppendUvarint(b, uint64(field<<3)), v)
}
func (b pb) bytes(field int, data []byte) pb {
	b = binary.AppendUvarint(b, uint64(field<<3|2))
	return append(binary.AppendUvarint(b, uint64(len(data))), data...)
}
func (b pb) str(field int, s string) pb { return b.bytes(field, []byte(s)) }

// valueInfo is a tensor input or output: its element type and shape, a dim named by a string or sized by an int.
func valueInfo(name string, elemType uint64, dims ...any) []byte {
	var shape pb
	for _, d := range dims {
		switch d := d.(type) {
		case int:
			shape = shape.bytes(1, pb{}.varint(1, uint64(d)))
		case string:
			shape = shape.bytes(1, pb{}.str(2, d))
		}
	}
	tensorType := pb{}.varint(1, elemType).bytes(2, shape)
	return pb{}.str(1, name).bytes(2, pb{}.bytes(1, tensorType))
}

// writeGatherModel writes a model whose last_hidden_state is table[input_ids], taking input_ids and attention_mask
// (and token_type_ids when withTypes).
func writeGatherModel(t *testing.T, table [][]float32, withTypes bool) string {
	t.Helper()
	dims := len(table[0])
	var floats []byte
	for _, row := range table {
		for _, f := range row {
			floats = binary.LittleEndian.AppendUint32(floats, math.Float32bits(f))
		}
	}
	initializer := pb{}.varint(1, uint64(len(table))).varint(1, uint64(dims)).varint(2, 1).str(8, "table").bytes(9, floats)
	node := pb{}.str(1, "table").str(1, "input_ids").str(2, "last_hidden_state").str(4, "Gather").str(3, "gather")
	graph := pb{}.bytes(1, node).str(2, "test").bytes(5, initializer).
		bytes(11, valueInfo("input_ids", 7, 1, "n")).bytes(11, valueInfo("attention_mask", 7, 1, "n"))
	if withTypes {
		graph = graph.bytes(11, valueInfo("token_type_ids", 7, 1, "n"))
	}
	graph = graph.bytes(12, valueInfo("last_hidden_state", 1, 1, "n", dims))
	model := pb{}.varint(1, 8).bytes(8, pb{}.str(1, "").varint(2, 17)).bytes(7, graph)
	path := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(path, model, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testRuntime(t *testing.T) *onnx.Runtime {
	t.Helper()
	path, err := onnx.FindRuntime(os.Getenv("ONNXRUNTIME"))
	if err != nil {
		t.Skip(err)
	}
	rt, err := loadRuntimeOnce(path)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func near(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-5 {
			return false
		}
	}
	return true
}

// The test vocabulary's ids: <s> 0, ▁ 4, ▁שלום 8, </s> 2.
var table = func() [][]float32 {
	rows := make([][]float32, 12)
	for i := range rows {
		rows[i] = []float32{0, 0}
	}
	rows[0] = []float32{3, 4} // <s>
	rows[2] = []float32{0, 2} // </s>
	rows[8] = []float32{6, 0} // ▁שלום
	return rows
}()

func TestBGEReadsTheFirstTokensVector(t *testing.T) {
	rt := testRuntime(t)
	e, err := NewEmbedder(rt, writeGatherModel(t, table, false), testTokenizer, BGEM3, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	v, err := e.Embed("שלום")
	if err != nil {
		t.Fatal(err)
	}
	if !near(v, []float32{0.6, 0.8}) {
		t.Fatal(v)
	}
}

func TestE5AveragesTheTokensWithItsPrefix(t *testing.T) {
	rt := testRuntime(t)
	spec := E5Small
	spec.Prefix = "" // the test vocabulary has no "query:"
	e, err := NewEmbedder(rt, writeGatherModel(t, table, true), testTokenizer, spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	v, err := e.Embed("שלום")
	if err != nil {
		t.Fatal(err)
	}
	// (3,4) + (6,0) + (0,2) = (9,6), unit length.
	if !near(v, []float32{9 / float32(math.Sqrt(117)), 6 / float32(math.Sqrt(117))}) {
		t.Fatal(v)
	}
}
