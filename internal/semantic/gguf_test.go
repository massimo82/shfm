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

package semantic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// ggufKV is one metadata entry of a synthetic GGUF file: its key and the
// already-encoded type + value.
type ggufKV struct {
	key   string
	typ   uint32
	value []byte
}

func ggufStr(s string) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(len(s)))
	return append(b, s...)
}

func kvString(key, v string) ggufKV { return ggufKV{key, ggufString, ggufStr(v)} }

func kvUint32(key string, v uint32) ggufKV {
	return ggufKV{key, ggufUint32, binary.LittleEndian.AppendUint32(nil, v)}
}

func kvUint64(key string, v uint64) ggufKV {
	return ggufKV{key, ggufUint64, binary.LittleEndian.AppendUint64(nil, v)}
}

// kvStringArray stands in for the tokenizer's token list: many small
// strings the parser has to walk over.
func kvStringArray(key string, n int) ggufKV {
	b := binary.LittleEndian.AppendUint32(nil, ggufString)
	b = binary.LittleEndian.AppendUint64(b, uint64(n))
	for i := 0; i < n; i++ {
		b = append(b, ggufStr("token"+strconv.Itoa(i))...)
	}
	return ggufKV{key, ggufArray, b}
}

func kvUint32Array(key string, n int) ggufKV {
	b := binary.LittleEndian.AppendUint32(nil, ggufUint32)
	b = binary.LittleEndian.AppendUint64(b, uint64(n))
	b = append(b, make([]byte, 4*n)...)
	return ggufKV{key, ggufArray, b}
}

// buildGGUF encodes a version-3 GGUF file with the given metadata and no
// tensors (the parser never gets that far).
func buildGGUF(kvs ...ggufKV) []byte {
	b := []byte("GGUF")
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = binary.LittleEndian.AppendUint64(b, 0)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(kvs)))
	for _, kv := range kvs {
		b = append(b, ggufStr(kv.key)...)
		b = binary.LittleEndian.AppendUint32(b, kv.typ)
		b = append(b, kv.value...)
	}
	return b
}

func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGGUFPoolingType(t *testing.T) {
	arch := kvString("general.architecture", "qwen3")
	tests := []struct {
		name    string
		content []byte
		want    int
		wantOK  bool
	}{
		{"embedding, last pooling", buildGGUF(arch, kvUint32("qwen3.embedding_length", 2560), kvUint32("qwen3.pooling_type", 3)), 3, true},
		{"reranker, rank pooling", buildGGUF(arch, kvUint32("qwen3.pooling_type", 4), kvStringArray("tokenizer.ggml.tokens", 10)), 4, true},
		{"pooling stored as uint64", buildGGUF(arch, kvUint64("bert.pooling_type", 2)), 2, true},
		{"after a big token list and a number array", buildGGUF(arch, kvStringArray("tokenizer.ggml.tokens", 20000), kvUint32Array("tokenizer.ggml.token_type", 5000), kvUint32("qwen3.pooling_type", 4)), 4, true},
		{"no pooling key", buildGGUF(arch, kvUint32("qwen3.block_count", 36), kvStringArray("tokenizer.ggml.tokens", 50)), 0, false},
		{"pooling key of the wrong type is ignored", buildGGUF(arch, kvString("qwen3.pooling_type", "last")), 0, false},
		{"no keys at all", buildGGUF(), 0, false},
	}
	for _, tc := range tests {
		got, ok, err := ggufPoolingType(writeTemp(t, tc.content))
		if err != nil || ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: got (%d, %v, %v), want (%d, %v, nil)", tc.name, got, ok, err, tc.want, tc.wantOK)
		}
	}
}

func TestGGUFPoolingTypeRejectsBadFiles(t *testing.T) {
	good := buildGGUF(kvString("general.architecture", "qwen3"), kvUint32("qwen3.pooling_type", 3))

	header := func(version uint32, kvCount uint64) []byte {
		b := []byte("GGUF")
		b = binary.LittleEndian.AppendUint32(b, version)
		b = binary.LittleEndian.AppendUint64(b, 0)
		return binary.LittleEndian.AppendUint64(b, kvCount)
	}
	hugeKey := binary.LittleEndian.AppendUint64(header(3, 1), 1<<40)
	unknownType := append(append(header(3, 1), ggufStr("k")...), binary.LittleEndian.AppendUint32(nil, 99)...)

	tests := []struct {
		name    string
		content []byte
		wantErr error // nil: any error
	}{
		{"not GGUF", []byte("this is not a model, just some text"), errNotGGUF},
		{"empty file", nil, errNotGGUF},
		{"old version", append([]byte("GGUF\x01\x00\x00\x00"), make([]byte, 16)...), nil},
		{"absurd key count", header(3, 1<<40), nil},
		{"absurd key length", hugeKey, nil},
		{"unknown value type", unknownType, nil},
		{"truncated inside the metadata", good[:len(good)-6], nil},
		{"truncated inside a token list", buildGGUF(kvStringArray("tokenizer.ggml.tokens", 100))[:200], nil},
	}
	for _, tc := range tests {
		_, ok, err := ggufPoolingType(writeTemp(t, tc.content))
		if err == nil || ok {
			t.Errorf("%s: want an error, got ok=%v err=%v", tc.name, ok, err)
			continue
		}
		if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
		}
	}

	if _, _, err := ggufPoolingType(filepath.Join(t.TempDir(), "missing.gguf")); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestRoleFromName(t *testing.T) {
	tests := map[string]modelRole{
		"Qwen3-Embedding-4B-Q8_0.gguf":       roleEmbedding,
		"qwen3-embedding-4b.gguf":            roleEmbedding,
		"nomic-embed-text-v1.5.Q4_K_M.gguf":  roleEmbedding,
		"Qwen3-Reranker-0.6B-Q4_K_M.gguf":    roleReranker,
		"bge-RERANK-v2-m3.gguf":              roleReranker,
		"model.gguf":                         roleUnknown,
		"Qwen3-4B-Instruct.gguf":             roleUnknown,
		"embedding-and-reranker-bundle.gguf": roleUnknown, // both: the name doesn't say
	}
	for name, want := range tests {
		if got := roleFromName(name); got != want {
			t.Errorf("roleFromName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRoleFromMetadata(t *testing.T) {
	arch := kvString("general.architecture", "qwen3")
	tests := []struct {
		name    string
		content []byte
		want    modelRole
	}{
		{"mean", buildGGUF(arch, kvUint32("bert.pooling_type", 1)), roleEmbedding},
		{"cls", buildGGUF(arch, kvUint32("bert.pooling_type", 2)), roleEmbedding},
		{"last", buildGGUF(arch, kvUint32("qwen3.pooling_type", 3)), roleEmbedding},
		{"rank", buildGGUF(arch, kvUint32("qwen3.pooling_type", 4)), roleReranker},
		{"none: a chat/base model", buildGGUF(arch, kvUint32("qwen3.pooling_type", 0)), roleUnknown},
		{"no pooling key", buildGGUF(arch), roleUnknown},
		{"unknown pooling value", buildGGUF(arch, kvUint32("qwen3.pooling_type", 9)), roleUnknown},
		{"not a GGUF file", bytes.Repeat([]byte("x"), 100), roleUnknown},
	}
	for _, tc := range tests {
		if got := roleFromMetadata(writeTemp(t, tc.content)); got != tc.want {
			t.Errorf("%s: roleFromMetadata = %v, want %v", tc.name, got, tc.want)
		}
	}
}
