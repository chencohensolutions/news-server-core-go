package stories

import (
	"sync"

	"github.com/chencohensolutions/news-server-core-go/onnx"
)

var (
	runtimeOnce   sync.Once
	loadedRuntime *onnx.Runtime
	runtimeErr    error
)

// loadRuntimeOnce loads ONNX Runtime once per process: a library can only be loaded once.
func loadRuntimeOnce(path string) (*onnx.Runtime, error) {
	runtimeOnce.Do(func() { loadedRuntime, runtimeErr = onnx.LoadRuntime(path) })
	return loadedRuntime, runtimeErr
}

// LoadRuntime is ONNX Runtime from path (config.json "onnxruntime"), else where onnx.FindRuntime looks.
func LoadRuntime(configured string) (*onnx.Runtime, error) {
	path, err := onnx.FindRuntime(configured)
	if err != nil {
		return nil, err
	}
	return loadRuntimeOnce(path)
}
