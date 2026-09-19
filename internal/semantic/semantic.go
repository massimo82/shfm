// Copyright (C) 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com>
//
// This file is part of shfm.
//
// shfm is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// shfm is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with shfm.  If not, see <https://www.gnu.org/licenses/>.

// Package semantic provides optional "semantic" content search — semantic
// search over file *contents* (text extracted from TXT/PDF/DOCX, embedded
// with a local LLM, matched by similarity) — as a self-contained, removable
// module: deleting this package plus its one call site in internal/ui
// (see semanticsearch.go) removes the feature entirely, with no effect on
// the rest of shfm.
//
// This file, and Engine/Result/Status below, are always compiled: they're
// the stable interface the rest of shfm programs against. The actual
// implementation is one of two build-tag-gated files:
//
//   - engine_disabled.go (default): a stub reporting the feature as
//     unavailable. No extra dependency, no cgo, nothing to build.
//
//   - engine_enabled.go (tag "semantic"): the real thing, using
//     github.com/tmc/langchaingo for text splitting and
//     github.com/tcpipuk/llama-go (a cgo binding to llama.cpp) to run a
//     local embedding model — and, optionally, a second local model to
//     rerank the embedding search's own top results (see Search's doc
//     comment on realEngine in engine_enabled.go for why that's worth
//     doing). Building it needs a C++ toolchain and isn't part of the
//     normal `go build .`; see the README's "Building" section for the
//     rest of the setup (a local llama-go checkout via go.work, the
//     embedding model, and optionally the reranker model).
package semantic

import (
	"context"
	"errors"
)

// Available reports whether this build was compiled with real semantic
// search support (the "semantic" build tag).
var Available bool

// ErrUnavailable is returned by every Engine method when shfm was built
// without the "semantic" tag (the default).
var ErrUnavailable = errors.New("semantic content search wasn't compiled into this build (rebuild with: go build -tags semantic . — see the README's \"Building\" section)")

// Result is one content match, RelPath relative to whatever root Search
// was asked to cover.
type Result struct {
	RelPath string
	Score   float64
}

// Status reports progress on an in-progress or just-finished index build,
// streamed on the channel passed to EnsureIndex.
type Status struct {
	Done, Total int
	Err         error
	Finished    bool
}

// Engine indexes a folder's text-extractable content and answers semantic
// queries against it, backed by a persistent on-disk index keyed by the
// folder's path, so returning to an already-indexed folder later reuses it
// instead of rebuilding from scratch.
type Engine interface {
	// EnsureIndex builds (or, if a usable one already exists on disk,
	// loads) the index for root, reporting progress until statusCh is
	// closed. Safe to call again while a build for the same root is
	// already running — that build is reused rather than duplicated.
	EnsureIndex(ctx context.Context, root string, statusCh chan<- Status)

	// HasIndex reports whether a usable index already exists for root (on
	// disk or already loaded in memory), without starting a build. An index
	// built with a different embedding model than the one configured now
	// isn't usable (its vectors can't be compared with the new model's), so
	// it counts as missing: EnsureIndex then rebuilds it from scratch.
	HasIndex(root string) bool

	// Search returns the topK best matches for query within root's index.
	Search(ctx context.Context, root, query string, topK int) ([]Result, error)
}

// New returns the engine for this build: the real one under the "semantic"
// tag, otherwise a stub whose methods report ErrUnavailable.
func New() Engine { return newEngine() }
