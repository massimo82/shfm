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

package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cases := []string{"", "s3gr3t0!", "password con spazi e àccenti", "🔒 emoji password"}
	for _, pw := range cases {
		enc, err := Encrypt(pw)
		if err != nil {
			t.Fatalf("Encrypt(%q) errore: %v", pw, err)
		}
		if pw == "" && enc != "" {
			t.Errorf("Encrypt(\"\") dovrebbe restituire stringa vuota, ottenuto %q", enc)
		}
		dec, err := Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt fallita per %q: %v", pw, err)
		}
		if dec != pw {
			t.Errorf("round-trip fallito: input=%q, decifrato=%q", pw, dec)
		}
	}
}

func TestEncryptedValueNotPlaintext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	pw := "password-molto-segreta"
	enc, err := Encrypt(pw)
	if err != nil {
		t.Fatal(err)
	}
	if enc == pw {
		t.Fatal("il valore cifrato non deve coincidere con il testo in chiaro")
	}
}

func TestDecryptCorrupted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if _, err := Decrypt("non-e-base64-valido!!!"); err == nil {
		t.Error("Decrypt su dato corrotto dovrebbe restituire un errore, non un panic o un valore a caso")
	}
}

func TestSeedPersistsAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	enc, err := Encrypt("prova")
	if err != nil {
		t.Fatal(err)
	}
	// una seconda chiamata deve riutilizzare lo stesso seme persistito su
	// disco, quindi il testo cifrato con la prima chiamata deve restare
	// decifrabile da una "nuova sessione" logica.
	dec, err := Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "prova" {
		t.Errorf("decifrato = %q, atteso 'prova'", dec)
	}

	seedPath := filepath.Join(dir, "shfm", ".keyseed")
	if _, err := os.Stat(seedPath); err != nil {
		t.Errorf("file del seme non trovato in %s: %v", seedPath, err)
	}
}
