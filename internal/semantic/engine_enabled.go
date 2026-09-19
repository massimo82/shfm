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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	chromem "github.com/philippgille/chromem-go"
	llama "github.com/tcpipuk/llama-go"
	"github.com/tmc/langchaingo/textsplitter"

	"shfm/internal/applog"
	"shfm/internal/semantic/extract"
)

func init() { Available = true }

const (
	// chunkSize/chunkOverlap are measured in *tokens* of the embedding
	// model's own tokenizer (see tokenLen), not characters — a raw
	// character count means a wildly different amount of actual content
	// depending on the text's language (English averages ~4 chars/token;
	// CJK scripts pack much more meaning per character, closer to
	// 1-2 chars/token), so "1000" wouldn't mean the same chunk size for
	// every document indexed. Token count is also what actually matters
	// for the model: it's what its (32K-token) context window is measured
	// in. 400/60 (15% overlap) sits in the low end of the commonly-cited
	// 256-512 token sweet spot for retrieval-oriented chunking — enough
	// content for a chunk to carry real context on its own, without
	// diluting a single embedding across too many unrelated ideas.
	chunkSize        = 400
	chunkOverlap     = 60
	maxIndexedFiles  = 20000
	collectionName   = "files"
	embeddingTimeout = 0 // no per-call timeout beyond ctx's own

	// rerankPoolSize caps how many of the embedding-ranked results actually
	// get reranked: each one costs a full model forward pass, so reranking
	// every result up to topK (200, see semanticsearch.go) would make a
	// search take tens of seconds. The rest of results keeps its embedding
	// order after this top slice — reranking matters most exactly there,
	// for what the user actually sees first.
	rerankPoolSize = 20
)

// retrievalInstruction is the task description both Qwen3-Embedding
// (embedQueryPrompt) and Qwen3-Reranker (rerankPrompt) expect on their
// query side — it's the same underlying task ("find passages relevant to
// this query") for both, so one shared instruction naturally covers both,
// and it's the Qwen team's own suggested default wording for a generic
// search scenario ("Given a web search query, retrieve relevant passages
// that answer the query" — reproduced verbatim, matters for the reranker
// since it was fine-tuned against this exact wording).
//
// rerankSystemPrompt is Qwen3-Reranker's own judgment template, also
// reproduced verbatim — see rerankPrompt.
const (
	retrievalInstruction = "Given a web search query, retrieve relevant passages that answer the query"
	rerankSystemPrompt   = `Judge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be "yes" or "no".`
)

// modelsDir is where the GGUF model files live. Semantic search never
// downloads a model on its own — a multi-gigabyte download is not something
// to trigger silently on a keypress — it only uses what the user put here.
func modelsDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "shfm", "models"), nil
}

// scanModels sorts the enabled model files in dir by role. A model is
// enabled when its name ends in ".gguf"; to keep a model around without using
// it, give it any other ending (".gguf.disabled"). Symlinks to files are
// followed; directories and anything else are ignored. A missing dir just
// means no models.
//
// Roles come from the file names first (roleFromName), which is free. Only
// for a role that no name matched are the remaining files (whose names say
// nothing) opened to read their GGUF metadata (roleFromMetadata); when the
// names already provide both roles, no file is opened at all. Files left
// without a role are returned in unknown, so the caller can say why they
// weren't used.
func scanModels(dir string) (byRole map[modelRole][]string, unknown []string, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	byRole = map[modelRole][]string{}
	var unnamed []string
	for _, ent := range entries {
		name := ent.Name()
		if !strings.EqualFold(filepath.Ext(name), ".gguf") {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		if role := roleFromName(name); role != roleUnknown {
			byRole[role] = append(byRole[role], p)
		} else {
			unnamed = append(unnamed, p)
		}
	}

	// Decided before looking at any metadata, so that two unnamed files that
	// both turn out to be embedding models are both found.
	needsMetadata := map[modelRole]bool{
		roleEmbedding: len(byRole[roleEmbedding]) == 0,
		roleReranker:  len(byRole[roleReranker]) == 0,
	}
	for _, p := range unnamed {
		if !needsMetadata[roleEmbedding] && !needsMetadata[roleReranker] {
			unknown = append(unknown, p)
			continue
		}
		if role := roleFromMetadata(p); needsMetadata[role] {
			byRole[role] = append(byRole[role], p)
		} else {
			unknown = append(unknown, p)
		}
	}
	for _, paths := range byRole {
		sort.Strings(paths)
	}
	sort.Strings(unknown)
	return byRole, unknown, nil
}

// pickModel returns the one enabled model with the wanted role: none is an
// error saying where to put one, and so is more than one — silently picking
// among several would mean nobody knows which model built an index or
// answered a query, so the user is told to disable the extras instead.
func pickModel(role modelRole) (string, error) {
	dir, err := modelsDir()
	if err != nil {
		return "", err
	}
	byRole, unknown, err := scanModels(dir)
	if err != nil {
		return "", err
	}
	found := byRole[role]
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		msg := fmt.Sprintf("no %s model found in %s: put a GGUF file ending in .gguf there "+
			"(see the README's \"Building\" section for which one to download)", role, dir)
		if len(unknown) > 0 {
			msg += fmt.Sprintf("; ignored, role unknown (no \"embed\"/\"rerank\" in the name, "+
				"and no embedding or reranker pooling type in the file): %s",
				strings.Join(baseNames(unknown), ", "))
		}
		return "", errors.New(msg)
	default:
		return "", fmt.Errorf("%w as %s in %s (%s): keep exactly one ending in .gguf "+
			"and rename the others, e.g. to .gguf.disabled",
			errSeveralModels, role, dir, strings.Join(baseNames(found), ", "))
	}
}

func baseNames(paths []string) []string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return names
}

// errSeveralModels is what pickModel wraps when a kind has more than one
// enabled model.
var errSeveralModels = errors.New("more than one model enabled")

// modelPath resolves the embedding model file to load: the one enabled
// embedding model in modelsDir.
func modelPath() (string, error) { return pickModel(roleEmbedding) }

// rerankModelPath resolves the separate, optional reranker model the same
// way. Unlike modelPath, a missing reranker isn't a hard error anywhere it's
// called from: reranking is a strict enhancement over embedding-only search
// (see Search's doc comment), so callers treat this failing as "reranking
// unavailable, carry on without it". An ambiguous choice (several enabled)
// is a misconfiguration rather than a routine absence, so that one is logged.
func rerankModelPath() (string, error) {
	p, err := pickModel(roleReranker)
	if errors.Is(err, errSeveralModels) {
		applog.Warn("semantic: reranking disabled", "reason", err)
	}
	return p, err
}

var logRedirectOnce sync.Once

// redirectLlamaLogging routes llama.cpp's own logging to a file instead of
// the terminal, and drops its verbosity to warnings and above.
//
// llama.cpp logs by calling fprintf(stderr, ...) straight from C++,
// bypassing Go (and thus bubbletea) entirely; shfm runs as a full-screen
// TUI holding the terminal in raw/alt-screen mode, so that raw text lands
// on top of the rendered UI and corrupts it. There's no way to change the
// destination through llama-go's API, so the whole process's stderr file
// descriptor is redirected at the OS level before the model is ever
// touched — safe here since nothing else in shfm writes to stderr.
//
// Respects an LLAMA_LOG already set in the environment (e.g. "debug" while
// troubleshooting this package itself), only defaulting it to "warn".
//
// llama-go reads LLAMA_LOG once, from its own package init() (necessarily
// before shfm's own code — including this function — ever runs), so
// setting the env var alone here would have no effect: llama.InitLogging()
// re-reads it and re-applies the level.
func redirectLlamaLogging() error {
	if os.Getenv("LLAMA_LOG") == "" {
		os.Setenv("LLAMA_LOG", "warn")
	}
	llama.InitLogging()
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(cacheDir, "shfm", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "llama.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	return syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}

// indexDir returns the persistent on-disk location of root's index,
// keyed by a hash of its absolute path.
func indexDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(cacheDir, "shfm", "semantic-index", hex.EncodeToString(sum[:])), nil
}

// fileMeta is what manifest remembers per indexed file, enough to detect a
// change (mtime+size, like rsync/make: a cheap stat(), no need to re-read
// and re-hash file content) and to find its chunk documents again (IDs are
// "<relPath>#0".."<relPath>#<Chunks-1>").
type fileMeta struct {
	ModTime int64 `json:"mtime"`
	Size    int64 `json:"size"`
	Chunks  int   `json:"chunks"`
}

// manifest is the staleness-tracking sidecar for a root's index, persisted
// as JSON next to chromem-go's own on-disk data (indexDir(root)). chromem-go
// doesn't expose a way to list a collection's documents, so shfm tracks
// per-file state itself instead.
//
// Model identifies the embedding model that produced the stored vectors
// (see modelIdentity). Vectors from different models aren't comparable — and
// usually don't even have the same length, which makes chromem-go fail every
// query with "vectors must have the same length" — so an index is only usable
// with the model it was built with; see EnsureIndex and HasIndex. Manifests
// written before this field existed (a bare path → fileMeta map) decode with
// an empty Model and are therefore rebuilt once.
type manifest struct {
	Model string              `json:"model"`
	Files map[string]fileMeta `json:"files"`
}

func manifestPath(dir string) string { return filepath.Join(dir, "manifest.json") }

func loadManifest(dir string) (manifest, error) {
	data, err := os.ReadFile(manifestPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return manifest{Files: map[string]fileMeta{}}, nil
	}
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		// Corrupt sidecar: treat as if nothing's tracked. Every file will
		// look "new" and get re-embedded, which is self-healing (chromem-go
		// documents are addressed by ID, so re-adding an existing ID just
		// overwrites it) rather than a hard failure.
		return manifest{Files: map[string]fileMeta{}}, nil
	}
	if m.Files == nil {
		m.Files = map[string]fileMeta{}
	}
	return m, nil
}

// modelFingerprintBytes is how much of the head of a model file modelIdentity
// hashes. A GGUF file starts with its metadata (architecture, name,
// finetune, tokenizer, ...), so this distinguishes models without reading
// gigabytes of weights on every Ctrl+F.
const modelFingerprintBytes = 1 << 20

// modelIdentity returns a stable identifier for the model file at path: its
// size plus a hash of its first modelFingerprintBytes. Deliberately not the
// path (renaming or moving the same file mustn't invalidate an index) and
// not the mtime (re-downloading the same file mustn't either), but it does
// change for a different model, size or quantization.
func modelIdentity(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, modelFingerprintBytes); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return fmt.Sprintf("%d-%s", st.Size(), hex.EncodeToString(h.Sum(nil))[:16]), nil
}

// currentModelIdentity is modelIdentity of the embedding model that
// modelPath resolves to right now.
func currentModelIdentity() (string, error) {
	path, err := modelPath()
	if err != nil {
		return "", err
	}
	return modelIdentity(path)
}

// indexMatchesModel reports whether root's persisted manifest was written
// with the embedding model currently configured, i.e. whether its stored
// vectors can be compared with the ones that model produces now.
func indexMatchesModel(root string) bool {
	id, err := currentModelIdentity()
	if err != nil {
		return false
	}
	dir, err := indexDir(root)
	if err != nil {
		return false
	}
	mf, err := loadManifest(dir)
	if err != nil {
		return false
	}
	return mf.Model == id
}

func saveManifest(dir string, m manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath(dir), data, 0o644)
}

// realEngine is the "semantic"-tag Engine: local GGUF model (via
// github.com/tcpipuk/llama-go) for embeddings, langchaingo for text
// chunking, and chromem-go (github.com/philippgille/chromem-go) — a pure-Go
// embedded vector database with on-disk persistence — for storage and
// similarity search.
type realEngine struct {
	mu    sync.Mutex
	model *llama.Model
	ectx  *llama.Context // a dedicated embeddings-mode context, lazily created

	rerankMu     sync.Mutex
	rerankModel  *llama.Model
	rerankCtx    *llama.Context // a dedicated rank-pooled context, lazily created
	rerankYesIdx int            // classifier output index for the "yes" label, resolved once rerankCtx loads

	dbsMu sync.Mutex
	dbs   map[string]*chromem.DB // by root, cached open handles

	indexingMu sync.Mutex
	indexing   map[string]bool // by root: a build is already running
}

func newEngine() Engine {
	return &realEngine{dbs: map[string]*chromem.DB{}, indexing: map[string]bool{}}
}

var logBackendOnce sync.Once

// logBackend records, once per process, which compute devices the linked
// llama.cpp build can actually use, so "why isn't the GPU being used?" can be
// answered from shfm.log instead of guessing. llama-go's own GPU reporting
// is CUDA-only, so this goes through ggml's generic device list. Logged at
// info level normally; at warn level when no GPU backend is present at all
// (the static libraries were built CPU-only — see the README's build notes),
// since then every model runs on the CPU regardless of hardware.
func logBackend() {
	logBackendOnce.Do(func() {
		var gpus, all []string
		for _, d := range llama.Devices() {
			desc := fmt.Sprintf("%s (%s, %d/%d MB free)", d.Name, d.Description, d.FreeMemoryMB, d.TotalMemoryMB)
			all = append(all, desc)
			if d.Type.IsGPU() {
				gpus = append(gpus, desc)
			}
		}
		if len(gpus) == 0 || !llama.SupportsGPUOffload() {
			applog.Warn("semantic: no GPU backend available, models will run on CPU only "+
				"(llama-go libraries built without Vulkan/CUDA/SYCL/...?)",
				"gpu_offload_supported", llama.SupportsGPUOffload(), "devices", all)
			return
		}
		applog.Info("semantic: GPU backend available, models are offloaded to it (CPU fallback otherwise)",
			"gpus", gpus, "devices", all)
	})
}

// ensureContext lazily loads the model (GPU offload with automatic CPU
// fallback — see modelPath/llama.WithGPULayers' -1 default) and a dedicated
// embeddings-mode context, once, reused for every index build and query.
func (e *realEngine) ensureContext() (*llama.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ectx != nil {
		return e.ectx, nil
	}
	path, err := modelPath()
	if err != nil {
		return nil, err
	}
	// Best-effort: if this fails, llama.cpp falls back to logging straight
	// to the terminal, same as before — not worth failing the model load over.
	logRedirectOnce.Do(func() { _ = redirectLlamaLogging() })
	logBackend()
	start := time.Now()
	model, err := llama.LoadModel(path, llama.WithSilentLoading())
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	applog.Info("semantic: embedding model loaded", "path", path, "took", time.Since(start).Round(time.Millisecond))
	ectx, err := model.NewContext(llama.WithEmbeddings())
	if err != nil {
		model.Close()
		return nil, fmt.Errorf("creating embedding context: %w", err)
	}
	e.model, e.ectx = model, ectx
	return ectx, nil
}

// ensureRerankContext lazily loads the (separate, optional) reranker model
// and a dedicated context for it, once. Unlike ensureContext (the embedding
// model, required for the feature to work at all), a failure here is
// routine — most installs simply won't have a reranker model configured —
// so this never gets treated as fatal by its callers.
func (e *realEngine) ensureRerankContext() (*llama.Context, error) {
	e.rerankMu.Lock()
	defer e.rerankMu.Unlock()
	if e.rerankCtx != nil {
		return e.rerankCtx, nil
	}
	path, err := rerankModelPath()
	if err != nil {
		return nil, err
	}
	logRedirectOnce.Do(func() { _ = redirectLlamaLogging() })
	logBackend()
	start := time.Now()
	model, err := llama.LoadModel(path, llama.WithSilentLoading())
	if err != nil {
		return nil, fmt.Errorf("loading reranker %s: %w", path, err)
	}
	applog.Info("semantic: reranker model loaded", "path", path, "took", time.Since(start).Round(time.Millisecond))
	yesIdx := -1
	for i := range model.NClsOut() {
		if strings.EqualFold(model.ClsLabel(i), "yes") {
			yesIdx = i
			break
		}
	}
	if yesIdx < 0 {
		model.Close()
		return nil, fmt.Errorf(
			"%s has no classifier head with a \"yes\" label — not a usable reranker GGUF "+
				"(a naive/community conversion is often missing the classifier tensor; needs one "+
				"converted with llama.cpp's official convert_hf_to_gguf.py)", path)
	}
	rctx, err := model.NewContext(llama.WithEmbeddings())
	if err != nil {
		model.Close()
		return nil, fmt.Errorf("creating rerank context: %w", err)
	}
	e.rerankModel, e.rerankCtx, e.rerankYesIdx = model, rctx, yesIdx
	return rctx, nil
}

// rerankPrompt reproduces Qwen3-Reranker's own judgment template verbatim,
// including the empty <think> block (the model was released to also do
// step-by-step reasoning before judging; shfm always skips straight to the
// answer for speed, which is the documented way to get its "non-thinking"
// behaviour).
func rerankPrompt(query, document string) string {
	return "<|im_start|>system\n" + rerankSystemPrompt + "<|im_end|>\n" +
		"<|im_start|>user\n<Instruct>: " + retrievalInstruction +
		"\n<Query>: " + query +
		"\n<Document>: " + document + "<|im_end|>\n" +
		"<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

// embedQueryPrompt wraps query in Qwen3-Embedding's own asymmetric
// instruction template for the query side of retrieval — document chunks
// are embedded as plain text with no prefix at all (the convention the
// model was actually trained on: only the query gets one). The model's own
// authors report that omitting this on the query side costs roughly 1-5%
// retrieval quality, for zero cost to add: it's pure string formatting
// before embedding, no extra model call, and index-time chunk embedding is
// entirely unaffected (see EnsureIndex, which embeds raw chunk text —
// unchanged).
func embedQueryPrompt(query string) string {
	return "Instruct: " + retrievalInstruction + "\nQuery: " + query
}

// rerankScore returns the reranker's "yes" (relevant) probability for
// (query, document) — already normalised by llama.cpp itself (see
// llama-go's GetRankScore doc comment), no further math needed here.
func (e *realEngine) rerankScore(rctx *llama.Context, query, document string) (float32, error) {
	// Mirrors embed's reasoning: one llama.cpp context processes one
	// request at a time, so callers (rerankResults, scoring one candidate
	// per call) are serialised here rather than via a worker pool.
	e.rerankMu.Lock()
	defer e.rerankMu.Unlock()
	scores, err := rctx.GetRankScore(rerankPrompt(query, document))
	if err != nil {
		return 0, err
	}
	if e.rerankYesIdx >= len(scores) {
		return 0, fmt.Errorf("reranker returned %d score(s), expected a \"yes\" at index %d", len(scores), e.rerankYesIdx)
	}
	return scores[e.rerankYesIdx], nil
}

// embed is the chromem.EmbeddingFunc backing every collection: it's handed
// to chromem-go once per collection and called by it for both indexing
// (each chunk) and querying (the search text), so callers never touch
// embeddings directly.
func (e *realEngine) embed(_ context.Context, text string) ([]float32, error) {
	ectx, err := e.ensureContext()
	if err != nil {
		return nil, err
	}
	// llama.cpp contexts process one request at a time; embedding calls are
	// short, so serializing them here is simpler than a worker pool and
	// costs nothing noticeable against the actual inference time.
	e.mu.Lock()
	vec, err := ectx.GetEmbeddings(text)
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeEmbedding(vec), nil
}

// normalizeEmbedding L2-normalizes vec in place (returning it, for chaining)
// before it reaches chromem-go. This matters because chromem-go's own
// normalization only ever runs on the *query* side (queryEmbedding always
// checks/normalizes) and on a *caller-supplied* document embedding — never
// on one it computed itself via the EmbeddingFunc (see AddDocument's
// `if len(doc.Embedding) == 0` branch, which stores whatever embed()
// returns verbatim). llama.cpp's raw Qwen3-Embedding output isn't unit
// length, and its magnitude varies noticeably by text (observed anywhere
// from ~13 to ~60 across a handful of short test documents) — so without
// this, every stored document vector carries its own magnitude into the
// query's dot product, silently corrupting "cosine similarity" into
// magnitude-weighted similarity: documents whose embeddings happen to have
// larger norms (not necessarily more *relevant* ones) would rank higher
// for every query, regardless of actual semantic closeness. Normalizing
// here — the one place both indexing and querying already funnel through —
// fixes it for both sides at once; chromem-go's own isNormalized check on
// the query side then becomes a (harmless, cheap) no-op.
func normalizeEmbedding(vec []float32) []float32 {
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if norm == 0 {
		return vec
	}
	for i, v := range vec {
		vec[i] = float32(float64(v) / norm)
	}
	return vec
}

// tokenLen is the splitter's length function (see EnsureIndex): the actual
// token count from the embedding model's own tokenizer, so chunkSize/
// chunkOverlap mean the same real content budget regardless of the text's
// language — see their doc comment for why that matters. Falls back to a
// Unicode-codepoint count (textsplitter's own original default) if
// tokenizing fails for any reason, e.g. the model isn't loaded yet — the
// splitter still needs *some* answer to keep working, and a slightly
// miscounted chunk boundary is harmless compared to failing the whole
// index build over it.
func (e *realEngine) tokenLen(text string) int {
	ectx, err := e.ensureContext()
	if err != nil {
		return utf8.RuneCountInString(text)
	}
	tokens, err := ectx.Tokenize(text)
	if err != nil {
		return utf8.RuneCountInString(text)
	}
	return len(tokens)
}

func (e *realEngine) collectionFor(root string) (*chromem.Collection, error) {
	dir, err := indexDir(root)
	if err != nil {
		return nil, err
	}
	e.dbsMu.Lock()
	db, ok := e.dbs[dir]
	if !ok {
		db, err = chromem.NewPersistentDB(dir, false)
		if err != nil {
			// A persisted index that fails to load — e.g. chromem-go's own
			// collection metadata file missing or corrupt, which can happen
			// if shfm was killed mid-write, since it isn't written
			// atomically — would otherwise break semantic search for root
			// permanently, with no way for the user to recover short of
			// finding and deleting this specific cache directory by hand.
			// Rebuilding is cheap (the actual documents on disk are
			// untouched; only the index needs re-embedding) and
			// self-healing, so wipe the unreadable index and try once more
			// with a fresh, empty one rather than failing for good.
			if rmErr := os.RemoveAll(dir); rmErr != nil {
				e.dbsMu.Unlock()
				return nil, fmt.Errorf("index for %s is unreadable (%v) and couldn't be reset: %w", root, err, rmErr)
			}
			db, err = chromem.NewPersistentDB(dir, false)
			if err != nil {
				e.dbsMu.Unlock()
				return nil, err
			}
		}
		e.dbs[dir] = db
	}
	e.dbsMu.Unlock()
	return db.GetOrCreateCollection(collectionName, nil, e.embed)
}

// resetIndex discards root's persisted index and any in-memory handle to it,
// and returns a fresh, empty collection in its place.
func (e *realEngine) resetIndex(root string) (*chromem.Collection, error) {
	dir, err := indexDir(root)
	if err != nil {
		return nil, err
	}
	// RemoveAll under the same lock collectionFor takes, so a concurrent
	// caller can't re-cache a handle to the directory being deleted.
	e.dbsMu.Lock()
	delete(e.dbs, dir)
	err = os.RemoveAll(dir)
	e.dbsMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("resetting the index for %s: %w", root, err)
	}
	return e.collectionFor(root)
}

// HasIndex reports false for an index built with a different embedding model
// than the one configured now, even though it exists on disk: it can't be
// searched (see manifest), so the caller should treat it as missing and
// call EnsureIndex, which rebuilds it.
func (e *realEngine) HasIndex(root string) bool {
	if !indexMatchesModel(root) {
		return false
	}
	col, err := e.collectionFor(root)
	if err != nil {
		return false
	}
	return col.Count() > 0
}

func (e *realEngine) EnsureIndex(ctx context.Context, root string, statusCh chan<- Status) {
	defer close(statusCh)

	e.indexingMu.Lock()
	if e.indexing[root] {
		e.indexingMu.Unlock()
		statusCh <- Status{Err: fmt.Errorf("already indexing %s", root), Finished: true}
		return
	}
	e.indexing[root] = true
	e.indexingMu.Unlock()
	defer func() {
		e.indexingMu.Lock()
		delete(e.indexing, root)
		e.indexingMu.Unlock()
	}()

	col, err := e.collectionFor(root)
	if err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}
	dir, err := indexDir(root)
	if err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}
	mf, err := loadManifest(dir)
	if err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}

	// An index is only valid for the embedding model that built it (see
	// manifest): if the configured model changed since, or the manifest
	// predates model tracking, throw the old vectors away and start over
	// instead of mixing embeddings from two models. Persisted right away, so
	// even a build cancelled midway is recognised as belonging to this model.
	modelID, err := currentModelIdentity()
	if err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}
	if mf.Model != modelID {
		if len(mf.Files) > 0 || col.Count() > 0 {
			applog.Info("semantic: embedding model changed since this index was built, rebuilding it", "root", root)
			if col, err = e.resetIndex(root); err != nil {
				statusCh <- Status{Err: err, Finished: true}
				return
			}
		}
		mf = manifest{Model: modelID, Files: map[string]fileMeta{}}
		if err := saveManifest(dir, mf); err != nil {
			statusCh <- Status{Err: err, Finished: true}
			return
		}
	}

	files, err := collectFiles(root)
	if err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}
	current := make(map[string]fileEntry, len(files))
	for _, f := range files {
		current[f.RelPath] = f
	}

	// Diff against the manifest with nothing but stat info (size+mtime),
	// the same cheap heuristic rsync/make use — no file content is read
	// here, so this stays fast even on a folder that's already indexed and
	// unchanged, which is the common case every time Ctrl+F reopens it.
	var toDeleteIDs []string
	for relPath, old := range mf.Files {
		cur, stillExists := current[relPath]
		if stillExists && cur.Size == old.Size && cur.ModTime == old.ModTime {
			continue // unchanged: leave its chunks and manifest entry alone
		}
		for i := range old.Chunks {
			toDeleteIDs = append(toDeleteIDs, fmt.Sprintf("%s#%d", relPath, i))
		}
		delete(mf.Files, relPath) // changed or removed: re-added below if it still exists
	}
	var toIndex []fileEntry
	for _, f := range files {
		if _, tracked := mf.Files[f.RelPath]; !tracked {
			toIndex = append(toIndex, f)
		}
	}

	if len(toDeleteIDs) == 0 && len(toIndex) == 0 {
		statusCh <- Status{Finished: true} // nothing changed since the last index
		return
	}

	// Fail fast on a missing/broken model here, rather than letting every
	// file's embed call fail silently below and ending up with a
	// "Finished" status that hides the fact that nothing got indexed.
	if _, err := e.ensureContext(); err != nil {
		statusCh <- Status{Err: err, Finished: true}
		return
	}

	if len(toDeleteIDs) > 0 {
		if err := col.Delete(ctx, nil, nil, toDeleteIDs...); err != nil {
			statusCh <- Status{Err: err, Finished: true}
			return
		}
	}

	splitter := textsplitter.NewRecursiveCharacter(
		textsplitter.WithChunkSize(chunkSize),
		textsplitter.WithChunkOverlap(chunkOverlap),
		textsplitter.WithLenFunc(e.tokenLen),
	)

	total := len(toIndex)
	for i, f := range toIndex {
		select {
		case <-ctx.Done():
			statusCh <- Status{Done: i, Total: total, Err: ctx.Err(), Finished: true}
			return
		default:
		}
		text, err := extract.Text(filepath.Join(root, f.RelPath))
		if err != nil || strings.TrimSpace(text) == "" {
			mf.Files[f.RelPath] = fileMeta{ModTime: f.ModTime, Size: f.Size}
			statusCh <- Status{Done: i + 1, Total: total}
			continue
		}
		chunks, err := splitter.SplitText(text)
		if err != nil {
			mf.Files[f.RelPath] = fileMeta{ModTime: f.ModTime, Size: f.Size}
			statusCh <- Status{Done: i + 1, Total: total}
			continue
		}
		docs := make([]chromem.Document, 0, len(chunks))
		for ci, chunk := range chunks {
			docs = append(docs, chromem.Document{
				ID:      fmt.Sprintf("%s#%d", f.RelPath, ci),
				Content: chunk,
				Metadata: map[string]string{
					"path": f.RelPath,
				},
			})
		}
		if len(docs) > 0 {
			if err := col.AddDocuments(ctx, docs, 1); err != nil {
				statusCh <- Status{Done: i + 1, Total: total, Err: err}
				continue
			}
		}
		mf.Files[f.RelPath] = fileMeta{ModTime: f.ModTime, Size: f.Size, Chunks: len(docs)}
		statusCh <- Status{Done: i + 1, Total: total}
	}

	// Manifest entries for files that vanished mid-run (deleted while
	// indexing) were already dropped above and never re-added: save
	// reflects reality either way.
	if err := saveManifest(dir, mf); err != nil {
		statusCh <- Status{Done: total, Total: total, Err: err, Finished: true}
		return
	}
	statusCh <- Status{Done: total, Total: total, Finished: true}
}

// Search returns root's topK best content matches for query: a first
// embedding-based retrieval pass (fast, whole-corpus, one vector comparison
// per chunk) followed by an optional reranking pass over its best few
// results (slow, one model forward pass per candidate, but meaningfully
// more precise — a cross-encoder scores query and document together in one
// pass instead of comparing two independently-computed vectors). Reranking
// only ever refines the top of an already-good list; if no reranker model
// is configured (the common case) or anything about it goes wrong, Search
// silently returns the embedding-only ordering instead of failing.
func (e *realEngine) Search(ctx context.Context, root, query string, topK int) ([]Result, error) {
	col, err := e.collectionFor(root)
	if err != nil {
		return nil, err
	}
	n := topK
	if c := col.Count(); c < n {
		n = c
	}
	if n <= 0 {
		return nil, nil
	}
	// embedQueryPrompt only wraps the text handed to the embedding model —
	// query itself stays untouched for reranking below, which builds its
	// own (differently-worded) prompt around the raw text.
	res, err := col.Query(ctx, embedQueryPrompt(query), n, nil, nil)
	if err != nil {
		return nil, err
	}
	// Multiple chunks can come from the same file: keep only the best
	// (highest-similarity) chunk per file, since Result is one row per file.
	bestByPath := map[string]float32{}
	bestContent := map[string]string{}
	order := []string{}
	for _, r := range res {
		path := r.Metadata["path"]
		if path == "" {
			continue
		}
		if prev, ok := bestByPath[path]; !ok || r.Similarity > prev {
			if !ok {
				order = append(order, path)
			}
			bestByPath[path] = r.Similarity
			bestContent[path] = r.Content
		}
	}
	results := make([]Result, 0, len(order))
	for _, path := range order {
		results = append(results, Result{RelPath: path, Score: float64(bestByPath[path])})
	}

	if rctx, err := e.ensureRerankContext(); err == nil {
		if reranked := e.rerankResults(ctx, rctx, query, results, bestContent); reranked != nil {
			results = reranked
		}
	}

	return results, nil
}

// minRerankRelevance is the reranker's own "yes" (relevant) probability
// below which a result is dropped rather than shown, instead of just
// sorted to the bottom. 0.5 (the mathematically "natural" yes/no cutoff)
// turned out too aggressive in practice: measured against real short,
// single-word queries, a genuinely-best, clearly-correct match can still
// land well under 0.5 (e.g. a document titled "Dichiarazione dei redditi
// ..." scored only 0.32 against the query "dichiarazione" — the model's
// own uncertainty about a short, generic term, not a sign the match is
// bad), while a truly-irrelevant pool (nothing in the index actually
// answers the query) clusters far lower still, around 0.001-0.01. 0.1 sits
// in the gap: comfortably above that noise floor (>10x margin against
// every irrelevant score observed), comfortably below every genuine match
// observed. Without this floor at all, a query that doesn't genuinely
// match anything in the index still returns a "top" result — whichever
// candidate happened to be least-unlike the query, even at a score like
// 0.005 (99.5% "no") — which looks, to someone glancing at result #1, like
// a confident match instead of noise. Only applied to the reranked pool:
// the never-reranked tail below rerankPoolSize keeps its raw embedding
// score, a different scale this threshold isn't calibrated for.
const minRerankRelevance = 0.1

// rerankResults re-scores the top rerankPoolSize of results (the rest are
// left as-is, in their embedding order, after it) using the reranker's
// classifier head, drops any that don't clear minRerankRelevance, and
// returns the combined, re-sorted list — or nil if reranking couldn't
// complete at all (context cancelled, a decode error, ...), telling Search
// to keep the embedding-only ordering it already has. An empty-but-non-nil
// result (every candidate judged irrelevant) is a valid, deliberate "no
// matches", distinct from that nil "reranking failed" case.
func (e *realEngine) rerankResults(ctx context.Context, rctx *llama.Context, query string, results []Result, content map[string]string) []Result {
	poolSize := min(len(results), rerankPoolSize)
	if poolSize == 0 {
		return nil
	}
	pool := results[:poolSize]
	rescored := make([]Result, poolSize)
	for i, r := range pool {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		score, err := e.rerankScore(rctx, query, content[r.RelPath])
		if err != nil {
			return nil // abort: fall back to the embedding ordering for everything
		}
		applog.Debug("rerank", "query", query, "path", r.RelPath, "embed", r.Score, "rerank", score)
		rescored[i] = Result{RelPath: r.RelPath, Score: float64(score)}
	}
	sort.Slice(rescored, func(i, j int) bool { return rescored[i].Score > rescored[j].Score })
	relevant := rescored[:0:0]
	for _, r := range rescored {
		if r.Score >= minRerankRelevance {
			relevant = append(relevant, r)
		}
	}
	combined := make([]Result, 0, len(relevant)+len(results)-poolSize)
	combined = append(combined, relevant...)
	combined = append(combined, results[poolSize:]...)
	return combined
}

// fileEntry is one extractable file found by collectFiles, carrying enough
// stat info (size+mtime) to compare against a manifest without re-reading
// the file's content.
type fileEntry struct {
	RelPath string
	Size    int64
	ModTime int64
}

// collectFiles walks root for extractable files (see extract.go),
// skipping hidden entries and never following symlinked directories (same
// reasoning as internal/ui's name-search: avoids an unbounded walk through
// a symlink cycle).
func collectFiles(root string) ([]fileEntry, error) {
	var files []fileEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries, don't abort the whole walk
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Type()&fs.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !extract.Supported(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // skip files we can't stat, don't abort the walk
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files = append(files, fileEntry{RelPath: rel, Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		if len(files) >= maxIndexedFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return files, err
}
