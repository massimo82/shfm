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

package drives

import (
	"crypto/rand"
	"crypto/sha512"
	"strings"
)

// cryptAlphabet is crypt(3)'s base 64 alphabet, salts included.
const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// sha512Crypt hashes password as crypt(3)'s SHA-512 method ("$6$") does,
// as openssl passwd -6 does, with a random salt of 16 characters and the
// default 5000 rounds.
func sha512Crypt(password string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, len(raw))
	for i, b := range raw {
		salt[i] = cryptAlphabet[b&0x3F]
	}
	return sha512CryptSalt(password, string(salt)), nil
}

// sha512CryptSalt is sha512Crypt with salt given (16 characters at most:
// more are dropped), following Ulrich Drepper's specification.
func sha512CryptSalt(password, salt string) string {
	const rounds = 5000
	p, s := []byte(password), []byte(salt[:min(len(salt), 16)])

	alt := sha512.New()
	alt.Write(p)
	alt.Write(s)
	alt.Write(p)
	b := alt.Sum(nil)

	a := sha512.New()
	a.Write(p)
	a.Write(s)
	n := len(p)
	for ; n > 64; n -= 64 {
		a.Write(b)
	}
	a.Write(b[:n])
	for n = len(p); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(b)
		} else {
			a.Write(p)
		}
	}
	c := a.Sum(nil)

	dp := sha512.New()
	for range len(p) {
		dp.Write(p)
	}
	pSeq := repeatTo(dp.Sum(nil), len(p))

	ds := sha512.New()
	for range 16 + int(c[0]) {
		ds.Write(s)
	}
	sSeq := repeatTo(ds.Sum(nil), len(s))

	for i := range rounds {
		r := sha512.New()
		if i&1 != 0 {
			r.Write(pSeq)
		} else {
			r.Write(c)
		}
		if i%3 != 0 {
			r.Write(sSeq)
		}
		if i%7 != 0 {
			r.Write(pSeq)
		}
		if i&1 != 0 {
			r.Write(c)
		} else {
			r.Write(pSeq)
		}
		c = r.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$" + string(s) + "$")
	put := func(b2, b1, b0 byte, chars int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for range chars {
			out.WriteByte(cryptAlphabet[w&0x3F])
			w >>= 6
		}
	}
	// The digest's bytes, in the order the specification shuffles them.
	for i := range 21 {
		put(c[(i*22)%63], c[(i*22+21)%63], c[(i*22+42)%63], 4)
	}
	put(0, 0, c[63], 2)
	return out.String()
}

// repeatTo is digest repeated, then cut, to n bytes.
func repeatTo(digest []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, digest[:min(len(digest), n-len(out))]...)
	}
	return out
}
