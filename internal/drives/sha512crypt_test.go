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
	"os/exec"
	"strings"
	"testing"
)

// The test vectors of the specification (rounds=5000, the default).
func TestSHA512CryptVectors(t *testing.T) {
	for _, c := range []struct{ salt, password, want string }{
		{"saltstring", "Hello world!", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
	} {
		if got := sha512CryptSalt(c.password, c.salt); got != c.want {
			t.Errorf("sha512CryptSalt(%q, %q) = %q, want %q", c.password, c.salt, got, c.want)
		}
	}
}

// The same as openssl passwd -6, for passwords long and short, where
// openssl is installed.
func TestSHA512CryptMatchesOpenSSL(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	for _, pw := range []string{"x", "raspberry", "è una password più lunga", strings.Repeat("long", 40)} {
		for _, salt := range []string{"ab", "0123456789abcdef"} {
			out, err := exec.Command(openssl, "passwd", "-6", "-salt", salt, pw).Output()
			if err != nil {
				t.Fatalf("openssl: %v", err)
			}
			if got, want := sha512CryptSalt(pw, salt), strings.TrimSpace(string(out)); got != want {
				t.Errorf("sha512CryptSalt(%q, %q) = %q, openssl %q", pw, salt, got, want)
			}
		}
	}
}

func TestSHA512CryptRandomSalt(t *testing.T) {
	a, err := sha512Crypt("pw")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := sha512Crypt("pw")
	if a == b || len(strings.Split(a, "$")[2]) != 16 {
		t.Errorf("hashes %q, %q: want different 16-character salts", a, b)
	}
}
