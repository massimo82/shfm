//go:build vulkan
// +build vulkan

// This file provides Vulkan GPU acceleration support when built with the
// 'vulkan' build tag. It links against the Vulkan API for cross-platform
// GPU-accelerated inference on NVIDIA, AMD, Intel, and ARM GPUs.
//
// Build with: BUILD_TYPE=vulkan make libbinding.a
//
// Requires Vulkan SDK installed with compatible GPU drivers. Vulkan provides
// a unified backend avoiding vendor-specific code whilst supporting modern GPU
// features including cooperative matrices and tensor cores.
//
// The link flags below are what this file's original comment listed as
// "required" but never actually declared: the static ggml-vulkan archive
// (copied next to the other libraries by the Makefile) is not part of
// linkage_static.go's library group, so it has to be linked here, inside its
// own group together with the archives it references.
//
// CGO flags required:
//
//	-lggml-vulkan -lvulkan
package llama

/*
#cgo LDFLAGS: -Wl,--start-group -lggml-vulkan -lggml-base -lggml -Wl,--end-group -lvulkan
*/
import "C"
