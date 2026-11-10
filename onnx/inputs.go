package onnx

import (
	"runtime"
	"unsafe"
)

// RunInputs gives the model several int64 inputs by name (a text model's input_ids, attention_mask and
// token_type_ids) and returns its outputs named outputs, in that order, which must be floats.
func RunInputs(s *Session, inputs map[string]Tensor[int64], outputs ...string) ([]Tensor[float32], error) {
	r := s.rt
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	inputNames := make([][]byte, len(names))
	inputPointers := make([]uintptr, len(names))
	values := make([]uintptr, len(names))
	for i, name := range names {
		in := inputs[name]
		inputNames[i] = cString(name)
		inputPointers[i] = uintptr(unsafe.Pointer(&inputNames[i][0]))
		if err := r.call(ortCreateTensorWithDataAsOrtValue, r.memory,
			uintptr(unsafe.Pointer(&in.Data[0])), uintptr(len(in.Data))*unsafe.Sizeof(in.Data[0]),
			uintptr(unsafe.Pointer(&in.Shape[0])), uintptr(len(in.Shape)), ortTensorInt64, uintptr(unsafe.Pointer(&values[i]))); err != nil {
			for _, v := range values[:i] {
				r.release(ortReleaseValue, v)
			}
			return nil, err
		}
	}
	for _, v := range values {
		defer r.release(ortReleaseValue, v)
	}
	outputNames := make([][]byte, len(outputs))
	outputPointers := make([]uintptr, len(outputs))
	for i, o := range outputs {
		outputNames[i] = cString(o)
		outputPointers[i] = uintptr(unsafe.Pointer(&outputNames[i][0]))
	}
	results := make([]uintptr, len(outputs))
	err := r.call(ortRun, s.handle, 0, uintptr(unsafe.Pointer(&inputPointers[0])), uintptr(unsafe.Pointer(&values[0])),
		uintptr(len(values)), uintptr(unsafe.Pointer(&outputPointers[0])), uintptr(len(outputs)), uintptr(unsafe.Pointer(&results[0])))
	// The names and data are held as plain numbers above, which the garbage collector doesn't see.
	runtime.KeepAlive(inputNames)
	runtime.KeepAlive(outputNames)
	runtime.KeepAlive(inputs)
	for _, v := range results {
		defer r.release(ortReleaseValue, v)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Tensor[float32], len(results))
	for i, v := range results {
		if out[i], err = r.floats(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}
