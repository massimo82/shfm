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
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

// This file works out what a model file is for — embedding or reranking —
// without loading it. roleFromName is the primary, free criterion;
// roleFromMetadata reads the start of the GGUF file itself and is the
// fallback scanModels uses only for a role no file name matched.

// modelRole is what a model file is used for.
type modelRole int

const (
	roleUnknown modelRole = iota
	roleEmbedding
	roleReranker
)

func (r modelRole) String() string {
	switch r {
	case roleEmbedding:
		return "embedding"
	case roleReranker:
		return "reranker"
	}
	return "unknown"
}

// roleFromName reads the role off the file name: "rerank"
// (Qwen3-Reranker-0.6B-Q4_K_M.gguf) means a reranker, "embed"
// (Qwen3-Embedding-4B-Q8_0.gguf) an embedding model. A name with neither, or
// with both, doesn't say: roleUnknown.
func roleFromName(name string) modelRole {
	n := strings.ToLower(name)
	rerank, embed := strings.Contains(n, "rerank"), strings.Contains(n, "embed")
	switch {
	case rerank && !embed:
		return roleReranker
	case embed && !rerank:
		return roleEmbedding
	}
	return roleUnknown
}

// roleFromMetadata reads the model's pooling type, which llama.cpp's own
// converter writes for exactly this purpose: a reranker (a classifier head
// scoring query and document together) is "rank"; an embedding model pools
// its hidden states into one vector ("mean", "cls" or "last"). A model
// without any pooling — a plain chat/base model — is neither, and
// unreadable or non-GGUF files aren't either.
func roleFromMetadata(path string) modelRole {
	pooling, ok, err := ggufPoolingType(path)
	if err != nil || !ok {
		return roleUnknown
	}
	switch pooling {
	case poolingRank:
		return roleReranker
	case poolingMean, poolingCLS, poolingLast:
		return roleEmbedding
	}
	return roleUnknown
}

// llama.cpp's enum llama_pooling_type, as stored in a GGUF file under
// "<architecture>.pooling_type".
const (
	poolingNone = 0
	poolingMean = 1
	poolingCLS  = 2
	poolingLast = 3
	poolingRank = 4
)

// GGUF value types (the metadata key-value section is a sequence of
// key, type, value; https://github.com/ggml-org/ggml/blob/master/docs/gguf.md).
const (
	ggufUint8   = 0
	ggufInt8    = 1
	ggufUint16  = 2
	ggufInt16   = 3
	ggufUint32  = 4
	ggufInt32   = 5
	ggufFloat32 = 6
	ggufBool    = 7
	ggufString  = 8
	ggufArray   = 9
	ggufUint64  = 10
	ggufInt64   = 11
	ggufFloat64 = 12
)

var ggufScalarSize = map[uint32]uint64{
	ggufUint8: 1, ggufInt8: 1, ggufBool: 1,
	ggufUint16: 2, ggufInt16: 2,
	ggufUint32: 4, ggufInt32: 4, ggufFloat32: 4,
	ggufUint64: 8, ggufInt64: 8, ggufFloat64: 8,
}

// Sanity limits so that a corrupt or hostile file can't make the parser
// loop or allocate without bound: real files have a few dozen keys, the
// longest is a tokenizer array (skipped without allocating anything).
const (
	ggufMaxKeys         = 1 << 20
	ggufMaxKeyLen       = 1 << 12
	ggufMaxArrayNesting = 2
)

var errNotGGUF = errors.New("not a GGUF file")

// ggufPoolingType returns the "<architecture>.pooling_type" metadata value
// of the GGUF file at path, and whether the file has one at all. Only the
// metadata section is read, and reading stops at that key: for the models
// used here it comes before the (multi-megabyte) tokenizer arrays.
func ggufPoolingType(path string) (pooling int, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)

	var magic [4]byte
	if _, err := readFull(r, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return 0, false, errNotGGUF
	}
	var version uint32
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return 0, false, err
	}
	if version < 2 || version > 3 {
		return 0, false, fmt.Errorf("unsupported GGUF version %d", version)
	}
	var tensors, keys uint64
	if err := binary.Read(r, binary.LittleEndian, &tensors); err != nil {
		return 0, false, err
	}
	if err := binary.Read(r, binary.LittleEndian, &keys); err != nil {
		return 0, false, err
	}
	if keys > ggufMaxKeys {
		return 0, false, fmt.Errorf("implausible GGUF key count %d", keys)
	}

	for i := uint64(0); i < keys; i++ {
		keyLen, err := readU64(r)
		if err != nil {
			return 0, false, err
		}
		if keyLen > ggufMaxKeyLen {
			return 0, false, fmt.Errorf("implausible GGUF key length %d", keyLen)
		}
		keyBuf := make([]byte, keyLen)
		if _, err := readFull(r, keyBuf); err != nil {
			return 0, false, err
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return 0, false, err
		}
		if strings.HasSuffix(string(keyBuf), ".pooling_type") {
			if v, isInt, err := readGGUFInt(r, typ); err != nil {
				return 0, false, err
			} else if isInt {
				return int(v), true, nil
			}
			continue // a non-integer under that name: already consumed by readGGUFInt
		}
		if err := skipGGUFValue(r, typ, 0); err != nil {
			return 0, false, err
		}
	}
	return 0, false, nil
}

func readU64(r *bufio.Reader) (uint64, error) {
	var v uint64
	err := binary.Read(r, binary.LittleEndian, &v)
	return v, err
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// readGGUFInt reads a value of type typ; if it's an integer type it returns
// its value, otherwise it skips the value and reports isInt=false.
func readGGUFInt(r *bufio.Reader, typ uint32) (v int64, isInt bool, err error) {
	switch typ {
	case ggufUint8, ggufInt8, ggufUint16, ggufInt16, ggufUint32, ggufInt32, ggufUint64, ggufInt64:
	default:
		return 0, false, skipGGUFValue(r, typ, 0)
	}
	buf := make([]byte, ggufScalarSize[typ])
	if _, err := readFull(r, buf); err != nil {
		return 0, false, err
	}
	switch typ {
	case ggufUint8:
		return int64(buf[0]), true, nil
	case ggufInt8:
		return int64(int8(buf[0])), true, nil
	case ggufUint16:
		return int64(binary.LittleEndian.Uint16(buf)), true, nil
	case ggufInt16:
		return int64(int16(binary.LittleEndian.Uint16(buf))), true, nil
	case ggufUint32:
		return int64(binary.LittleEndian.Uint32(buf)), true, nil
	case ggufInt32:
		return int64(int32(binary.LittleEndian.Uint32(buf))), true, nil
	}
	return int64(binary.LittleEndian.Uint64(buf)), true, nil // 64-bit types
}

// skipGGUFValue advances r past one value of type typ without keeping it.
func skipGGUFValue(r *bufio.Reader, typ uint32, depth int) error {
	if size, ok := ggufScalarSize[typ]; ok {
		return discard(r, size)
	}
	switch typ {
	case ggufString:
		n, err := readU64(r)
		if err != nil {
			return err
		}
		return discard(r, n)
	case ggufArray:
		if depth >= ggufMaxArrayNesting {
			return errors.New("GGUF array nested too deeply")
		}
		var elem uint32
		if err := binary.Read(r, binary.LittleEndian, &elem); err != nil {
			return err
		}
		n, err := readU64(r)
		if err != nil {
			return err
		}
		if size, ok := ggufScalarSize[elem]; ok {
			if n > (1<<62)/size {
				return errors.New("implausible GGUF array length")
			}
			return discard(r, n*size)
		}
		for i := uint64(0); i < n; i++ {
			if err := skipGGUFValue(r, elem, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown GGUF value type %d", typ)
}

// discard skips n bytes, failing with an error (rather than blocking or
// wrapping) if the file ends first or n is absurd.
func discard(r *bufio.Reader, n uint64) error {
	if n > 1<<62 {
		return errors.New("implausible GGUF value length")
	}
	for n > 0 {
		step := int(min(n, 1<<30))
		got, err := r.Discard(step)
		n -= uint64(got)
		if err != nil {
			return err
		}
	}
	return nil
}
