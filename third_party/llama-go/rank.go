package llama

/*
#include "wrapper.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// NClsOut returns the number of classifier output scores this model
// produces (e.g. 2 for a Qwen3-Reranker's yes/no head), or -1 if the model
// has no classifier head at all (i.e. it isn't a reranker/classifier model).
func (m *Model) NClsOut() int {
	return int(C.llama_wrapper_model_n_cls_out(m.modelPtr))
}

// ClsLabel returns the label for classifier output index i (< NClsOut()),
// e.g. "yes" or "no" for Qwen3-Reranker. Returns "" if the model provides no
// label for that index.
func (m *Model) ClsLabel(i int) string {
	cs := C.llama_wrapper_model_cls_label(m.modelPtr, C.int(i))
	if cs == nil {
		return ""
	}
	return C.GoString(cs)
}

// GetRankScore returns the classifier's relevance score(s) for text — the
// context must be created with WithEmbeddings() against a model whose GGUF
// declares pooling_type == RANK (a genuine reranker conversion; see
// Model.NClsOut/ClsLabel to introspect what's actually loaded, since a
// naively-converted reranker GGUF often lacks the classifier head entirely).
//
// Qwen3 rerankers apply softmax over their classifier logits inside
// llama.cpp's own graph (see llama-graph.cpp's build_pooling, the
// LLAMA_POOLING_TYPE_RANK case), so the values returned here are already
// normalised probabilities — no further softmax/sigmoid needed by the
// caller.
func (c *Context) GetRankScore(text string) ([]float32, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return nil, fmt.Errorf("context is closed")
	}

	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))

	maxScores := 8
	scores := make([]C.float, maxScores)

	count := C.llama_wrapper_rank_score(c.contextPtr, cText, &scores[0], C.int(maxScores))
	if count < 0 {
		return nil, fmt.Errorf("rank score generation failed: %s", C.GoString(C.llama_wrapper_last_error()))
	}

	result := make([]float32, count)
	for i := 0; i < int(count); i++ {
		result[i] = float32(scores[i])
	}
	return result, nil
}

// DeviceType classifies a ggml compute device, mirroring ggml's
// ggml_backend_dev_type.
type DeviceType int

const (
	DeviceCPU   DeviceType = 0 // the host CPU
	DeviceGPU   DeviceType = 1 // a discrete GPU
	DeviceIGPU  DeviceType = 2 // an integrated GPU (shares system RAM)
	DeviceAccel DeviceType = 3 // an accelerator (e.g. BLAS/AMX), used alongside the CPU
	DeviceMeta  DeviceType = 4 // a meta device wrapping several others
)

// IsGPU reports whether the device is a discrete or integrated GPU.
func (t DeviceType) IsGPU() bool { return t == DeviceGPU || t == DeviceIGPU }

// Device describes one compute device ggml can run on.
type Device struct {
	Name          string // backend's own name, e.g. "Vulkan0", "CPU"
	Description   string // device model, e.g. "Intel(R) Iris(R) Xe Graphics (ADL GT2)"
	Type          DeviceType
	FreeMemoryMB  int
	TotalMemoryMB int
}

// Devices lists every compute device the linked ggml backends registered,
// via ggml's generic device registry. Unlike Model.Stats().GPUs (CUDA-only),
// this reports whatever GPU backend the static libraries were actually
// built with (Vulkan, SYCL, OpenCL, Metal, ...). A build without any GPU
// backend yields only the CPU entries.
func Devices() []Device {
	n := int(C.llama_wrapper_device_count())
	devs := make([]Device, 0, n)
	for i := 0; i < n; i++ {
		var ci C.llama_wrapper_device_info
		if !C.llama_wrapper_device_get(C.int(i), &ci) {
			continue
		}
		devs = append(devs, Device{
			Name:          C.GoString(&ci.name[0]),
			Description:   C.GoString(&ci.description[0]),
			Type:          DeviceType(ci._type),
			FreeMemoryMB:  int(ci.free_memory_mb),
			TotalMemoryMB: int(ci.total_memory_mb),
		})
	}
	return devs
}

// SupportsGPUOffload reports whether llama.cpp was built with a GPU backend
// that can offload model layers (false for a CPU-only build).
func SupportsGPUOffload() bool {
	return bool(C.llama_wrapper_supports_gpu_offload())
}
