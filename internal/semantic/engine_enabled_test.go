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

//go:build semantic

package semantic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// writeFakeModel creates a stand-in "model" file: modelIdentity only looks at
// its size and head, so no real GGUF is needed (and none of these tests load
// llama.cpp).
func writeFakeModel(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModelIdentity(t *testing.T) {
	a := bytes.Repeat([]byte{'a'}, 2<<20)
	b := bytes.Repeat([]byte{'b'}, 2<<20)
	aHeadDiffers := append([]byte("different-name"), a[len("different-name"):]...)
	aTailDiffers := append(append([]byte{}, a[:len(a)-1]...), 'z')

	id := func(name string, content []byte) string {
		t.Helper()
		got, err := modelIdentity(writeFakeModel(t, name, content))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	base := id("qwen3-embedding-4b.gguf", a)
	if got := id("renamed-elsewhere.gguf", a); got != base {
		t.Errorf("same content under another name must keep its identity: %q vs %q", got, base)
	}
	if got := id("m.gguf", b); got == base {
		t.Errorf("different content must change the identity, both %q", got)
	}
	if got := id("m.gguf", a[:1<<20+5]); got == base {
		t.Errorf("different size must change the identity, both %q", got)
	}
	if got := id("m.gguf", aHeadDiffers); got == base {
		t.Errorf("same size but different header must change the identity, both %q", got)
	}
	// Documented tradeoff: only the head and the size are looked at.
	if got := id("m.gguf", aTailDiffers); got != base {
		t.Errorf("a change past the fingerprinted head is not expected to be detected: %q vs %q", got, base)
	}

	if _, err := modelIdentity(filepath.Join(t.TempDir(), "missing.gguf")); err == nil {
		t.Error("a missing model file must be an error")
	}
	// A file shorter than the fingerprint window is fine.
	if _, err := modelIdentity(writeFakeModel(t, "tiny.gguf", []byte("x"))); err != nil {
		t.Errorf("short file: %v", err)
	}
}

func TestManifestRoundTripAndLegacy(t *testing.T) {
	dir := t.TempDir()

	// No manifest yet: empty, usable, no model.
	m, err := loadManifest(dir)
	if err != nil || m.Model != "" || m.Files == nil || len(m.Files) != 0 {
		t.Fatalf("missing manifest = %+v, %v", m, err)
	}

	want := manifest{Model: "123-abc", Files: map[string]fileMeta{"a.txt": {ModTime: 1, Size: 2, Chunks: 3}}}
	if err := saveManifest(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadManifest(dir)
	if err != nil || got.Model != want.Model || got.Files["a.txt"] != want.Files["a.txt"] {
		t.Fatalf("round trip = %+v, %v", got, err)
	}

	// Pre-model-tracking format: a bare path → fileMeta map. Must load
	// without error but with no model, so it is rebuilt rather than trusted.
	legacy := `{"a.txt":{"mtime":1,"size":2,"chunks":3}}`
	if err := os.WriteFile(manifestPath(dir), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = loadManifest(dir)
	if err != nil || got.Model != "" || got.Files == nil || len(got.Files) != 0 {
		t.Fatalf("legacy manifest = %+v, %v", got, err)
	}

	// Corrupt manifest: same self-healing treatment.
	if err := os.WriteFile(manifestPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = loadManifest(dir)
	if err != nil || got.Model != "" || got.Files == nil {
		t.Fatalf("corrupt manifest = %+v, %v", got, err)
	}
}

// indexedRoot builds a root whose index holds one document (with a
// caller-supplied embedding, so no model is needed) and a manifest stamped
// with the given model identity.
func indexedRoot(t *testing.T, e *realEngine, modelID string) string {
	t.Helper()
	root := t.TempDir()
	col, err := e.collectionFor(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := chromem.Document{ID: "a.txt#0", Content: "hello", Embedding: []float32{1, 0}, Metadata: map[string]string{"path": "a.txt"}}
	if err := col.AddDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	dir, err := indexDir(root)
	if err != nil {
		t.Fatal(err)
	}
	mf := manifest{Model: modelID, Files: map[string]fileMeta{"a.txt": {ModTime: 1, Size: 5, Chunks: 1}}}
	if err := saveManifest(dir, mf); err != nil {
		t.Fatal(err)
	}
	return root
}

// putModel drops a fake model file of the given size into the models
// directory (under the XDG_CACHE_HOME the test has set) and returns its path.
func putModel(t *testing.T, name string, size int) string {
	t.Helper()
	dir, err := modelsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte{byte(size)}, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHasIndexDependsOnModel(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	small := putModel(t, "embed-small.gguf", 1000)
	smallID, err := modelIdentity(small)
	if err != nil {
		t.Fatal(err)
	}

	e := newEngine().(*realEngine)
	root := indexedRoot(t, e, smallID)

	if !e.HasIndex(root) {
		t.Error("index built with the enabled model must be usable")
	}

	// Swap models the way users do: disable one, enable another.
	if err := os.Rename(small, small+".disabled"); err != nil {
		t.Fatal(err)
	}
	large := putModel(t, "embed-large.gguf", 5000)
	if e.HasIndex(root) {
		t.Error("index built with another model must count as missing")
	}

	if err := os.Remove(large); err != nil {
		t.Fatal(err)
	}
	if e.HasIndex(root) {
		t.Error("with no model enabled there is no usable index")
	}

	// Back to the original model: the index is valid again, unless its
	// manifest has no model recorded (legacy/corrupt), which is never trusted.
	if err := os.Rename(small+".disabled", small); err != nil {
		t.Fatal(err)
	}
	if !e.HasIndex(root) {
		t.Error("re-enabling the original model must make its index usable again")
	}
	dir, _ := indexDir(root)
	if err := os.WriteFile(manifestPath(dir), []byte(`{"a.txt":{"mtime":1,"size":5,"chunks":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if e.HasIndex(root) {
		t.Error("a legacy manifest without a model must not be trusted")
	}
}

func TestModelDiscovery(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// No models directory at all.
	if _, err := modelPath(); err == nil {
		t.Error("no models directory: modelPath must fail")
	}
	if _, err := rerankModelPath(); err == nil {
		t.Error("no models directory: rerankModelPath must fail")
	}

	emb := putModel(t, "Qwen3-Embedding-4B-Q8_0.gguf", 10)
	rer := putModel(t, "Qwen3-Reranker-0.6B-Q4_K_M.gguf", 20)
	// None of these is an enabled model.
	putModel(t, "Qwen3-Embedding-0.6B-Q8_0.gguf.disabled", 30)
	putModel(t, "notes.txt", 40)
	dir, _ := modelsDir()
	if err := os.Mkdir(filepath.Join(dir, "a-directory.gguf"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, err := modelPath(); err != nil || got != emb {
		t.Errorf("modelPath = %q, %v; want %q", got, err, emb)
	}
	if got, err := rerankModelPath(); err != nil || got != rer {
		t.Errorf("rerankModelPath = %q, %v; want %q", got, err, rer)
	}

	// The extension is matched case-insensitively and a symlink to a model
	// elsewhere counts.
	target := writeFakeModel(t, "elsewhere.bin", []byte("model"))
	link := filepath.Join(dir, "Linked-Reranker.GGUF")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := rerankModelPath(); !errors.Is(err, errSeveralModels) {
		t.Errorf("two enabled rerankers must be ambiguous, got %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	// Two enabled embedding models: an error naming both, not a silent pick.
	putModel(t, "Qwen3-Embedding-0.6B-Q8_0.gguf", 30)
	_, err := modelPath()
	if !errors.Is(err, errSeveralModels) {
		t.Fatalf("two enabled embedding models must be ambiguous, got %v", err)
	}
	for _, want := range []string{"Qwen3-Embedding-4B-Q8_0.gguf", "Qwen3-Embedding-0.6B-Q8_0.gguf", ".gguf.disabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	// The reranker is unaffected by the embedding models' ambiguity.
	if got, err := rerankModelPath(); err != nil || got != rer {
		t.Errorf("rerankModelPath = %q, %v; want %q", got, err, rer)
	}
}

func TestResetIndex(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	e := newEngine().(*realEngine)
	root := indexedRoot(t, e, "old-model")

	col, err := e.collectionFor(root)
	if err != nil || col.Count() != 1 {
		t.Fatalf("before reset: count %d, %v", col.Count(), err)
	}
	fresh, err := e.resetIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Count() != 0 {
		t.Errorf("after reset the collection has %d documents, want 0", fresh.Count())
	}
	dir, _ := indexDir(root)
	if _, err := os.Stat(manifestPath(dir)); !os.IsNotExist(err) {
		t.Errorf("the old manifest must be gone after a reset (stat err: %v)", err)
	}
	// The fresh collection is usable, with vectors of a different length
	// than the discarded ones — the case that made the old index unusable.
	doc := chromem.Document{ID: "b.txt#0", Content: "hi", Embedding: []float32{0, 0, 1}, Metadata: map[string]string{"path": "b.txt"}}
	if err := fresh.AddDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	res, err := fresh.QueryEmbedding(context.Background(), []float32{0, 0, 1}, 1, nil, nil)
	if err != nil || len(res) != 1 || res[0].ID != "b.txt#0" {
		t.Fatalf("query after reset = %+v, %v", res, err)
	}
}

// putFile writes content under name in the models directory (under the
// XDG_CACHE_HOME the test has set) and returns the path.
func putFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir, err := modelsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// pooled is a minimal GGUF whose metadata declares the given pooling type.
func pooled(pooling uint32) []byte {
	return buildGGUF(kvString("general.architecture", "qwen3"), kvUint32("qwen3.pooling_type", pooling))
}

// The GGUF metadata is only a fallback for a role the file names didn't
// provide.
func TestModelDiscoveryMetadataFallback(t *testing.T) {
	t.Run("names give the embedding, metadata finds the reranker", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		emb := putFile(t, "Qwen3-Embedding-4B-Q8_0.gguf", []byte("not even a GGUF: the name is enough"))
		rer := putFile(t, "model.gguf", pooled(poolingRank))
		if got, err := modelPath(); err != nil || got != emb {
			t.Errorf("modelPath = %q, %v; want %q", got, err, emb)
		}
		if got, err := rerankModelPath(); err != nil || got != rer {
			t.Errorf("rerankModelPath = %q, %v; want %q", got, err, rer)
		}
	})

	t.Run("names give both roles: unnamed files are not even opened", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		emb := putFile(t, "Qwen3-Embedding-4B-Q8_0.gguf", []byte("x"))
		rer := putFile(t, "Qwen3-Reranker-0.6B-Q4_K_M.gguf", []byte("x"))
		// Metadata says embedding, but that role is already taken by name:
		// it must not become a second one (which would be ambiguous).
		putFile(t, "other.gguf", pooled(poolingLast))
		if got, err := modelPath(); err != nil || got != emb {
			t.Errorf("modelPath = %q, %v; want %q", got, err, emb)
		}
		if got, err := rerankModelPath(); err != nil || got != rer {
			t.Errorf("rerankModelPath = %q, %v; want %q", got, err, rer)
		}
	})

	t.Run("nothing named: metadata gives both roles", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		emb := putFile(t, "a.gguf", pooled(poolingLast))
		rer := putFile(t, "b.gguf", pooled(poolingRank))
		if got, err := modelPath(); err != nil || got != emb {
			t.Errorf("modelPath = %q, %v; want %q", got, err, emb)
		}
		if got, err := rerankModelPath(); err != nil || got != rer {
			t.Errorf("rerankModelPath = %q, %v; want %q", got, err, rer)
		}
	})

	t.Run("two unnamed embedding models are ambiguous", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		putFile(t, "a.gguf", pooled(poolingMean))
		putFile(t, "b.gguf", pooled(poolingLast))
		if _, err := modelPath(); !errors.Is(err, errSeveralModels) {
			t.Errorf("modelPath err = %v, want errSeveralModels", err)
		}
	})

	t.Run("a chat model and junk are ignored, and the error says so", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		putFile(t, "chat-model.gguf", pooled(poolingNone))
		putFile(t, "junk.gguf", []byte("not a GGUF"))
		_, err := modelPath()
		if err == nil {
			t.Fatal("no embedding model: modelPath must fail")
		}
		for _, want := range []string{"chat-model.gguf", "junk.gguf", "no embedding model found"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
		if _, err := rerankModelPath(); err == nil {
			t.Error("no reranker model: rerankModelPath must fail")
		}
	})
}
