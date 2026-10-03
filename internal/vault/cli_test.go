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

//go:build vault

package vault

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"shfm/internal/toolpath"
	"shfm/internal/vfs"
)

// tool finds a command line tool for the recovery tests: the path in
// $SHFM_TEST_<NAME> if set (e.g. SHFM_TEST_JQ=/path/to/gojq), else where
// shfm would find it. The test is skipped without it (CI installs them).
func tool(t *testing.T, name string) string {
	t.Helper()
	env := "SHFM_TEST_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if p := os.Getenv(env); p != "" {
		return p
	}
	p, err := toolpath.Find(name)
	if err != nil {
		t.Skipf("%s not found (or set %s)", name, env)
	}
	return p
}

// recoveryScript returns the indented shell block of RECOVERY.txt that
// starts with the line first, up to and including the line last, with the
// commands replaced by the tools' paths.
func recoveryScript(t *testing.T, dir, first, last string, tools map[string]string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(dir, recoveryFile))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(text), first)
	end := strings.Index(string(text), last)
	if start < 0 || end < start {
		t.Fatalf("RECOVERY.txt has no block %q…%q", first, last)
	}
	block := string(text)[start : end+len(last)]
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		lines = append(lines, strings.TrimPrefix(l, "    "))
	}
	script := strings.Join(lines, "\n")
	for name, p := range tools {
		script = regexp.MustCompile(`(^|[\s|('])`+name+` `).ReplaceAllString(script, "${1}"+p+" ")
	}
	return script
}

// treeOf maps every file under root, by relative path, to its content.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			data, _ := os.ReadFile(p)
			out[rel] = string(data)
		}
		return nil
	})
	return out
}

func fillVault(t *testing.T, fs vfs.FileSystem) map[string]string {
	want := map[string]string{
		"top.txt":                     "top",
		"with space.txt":              "spaced",
		"docs/report final.pdf":       string(randBytes(3*chunkSize + 1)),
		"docs/deep/nested/empty file": "",
	}
	for _, d := range []string{"/docs", "/docs/deep", "/docs/deep/nested"} {
		if err := fs.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	for p, data := range want {
		writeFile(t, fs, "/"+p, []byte(data))
	}
	return want
}

func runScript(t *testing.T, sh, dir, script string) {
	t.Helper()
	cmd := exec.Command(sh, "-c", script)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery script: %v\n%s\n--- script:\n%s", err, out, script)
	}
}

// The whole vault comes back with RECOVERY.txt's script, age and jq only.
func TestRecoveryEncryptedNames(t *testing.T) {
	ageCmd, jqCmd, sh := tool(t, "age"), tool(t, "jq"), tool(t, "sh")
	_, dir, v, fs := newVault(t, Options{})
	want := fillVault(t, fs)
	key, _ := v.RecoveryKey()
	os.WriteFile(filepath.Join(dir, "key.txt"), []byte(key+"\n"), 0o600)

	out := filepath.Join(t.TempDir(), "recovered")
	script := recoveryScript(t, dir, "KEY=key.txt", "restore . ../recovered", map[string]string{"age": ageCmd, "jq": jqCmd})
	script = strings.Replace(script, "restore . ../recovered", "restore . '"+out+"'", 1)
	runScript(t, sh, dir, script)
	if got := treeOf(t, out); !equalTrees(got, want) {
		t.Fatalf("recovered tree differs: %v", keys(got))
	}
}

func TestRecoveryPlainNames(t *testing.T) {
	ageCmd, find, sh := tool(t, "age"), tool(t, "find"), tool(t, "sh")
	_, dir, v, fs := newVault(t, Options{Names: NamesPlain})
	want := fillVault(t, fs)
	key, _ := v.RecoveryKey()
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	os.WriteFile(keyFile, []byte(key+"\n"), 0o600)

	script := recoveryScript(t, dir, "find . -name", "sh {} \\;", map[string]string{"age": ageCmd, "find": find})
	script = strings.ReplaceAll(script, "key.txt", keyFile)
	runScript(t, sh, dir, script)
	got := treeOf(t, dir)
	for p, data := range want {
		if got[p] != data {
			t.Fatalf("%s not recovered in place", p)
		}
	}
}

// A post-quantum vault: age (1.3+) decrypts its files with the key as is.
func TestRecoveryPostQuantum(t *testing.T) {
	ageCmd := tool(t, "age")
	if v, _ := exec.Command(ageCmd, "--version").Output(); regexp.MustCompile(`^v?1\.[0-2]\.`).Match(v) {
		t.Skipf("age %s predates post-quantum keys (1.3.0)", bytes.TrimSpace(v))
	}
	_, dir, v, fs := newVault(t, Options{Names: NamesPlain, PostQuantum: true})
	writeFile(t, fs, "/a.txt", []byte("quantum safe"))
	key, _ := v.RecoveryKey()
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	os.WriteFile(keyFile, []byte(key+"\n"), 0o600)
	out, err := exec.Command(ageCmd, "-d", "-i", keyFile, filepath.Join(dir, "a.txt.age")).Output()
	if err != nil || string(out) != "quantum safe" {
		t.Fatalf("age -d with the post-quantum key: %q, %v", out, err)
	}
	if text, _ := os.ReadFile(filepath.Join(dir, recoveryFile)); !bytes.Contains(text, []byte("age-plugin-pq -identity")) {
		t.Fatal("RECOVERY.txt of a post-quantum vault doesn't explain the plugin")
	}
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A split vault comes back from any two parts with the Python script of
// the parts' RECOVERY.txt, then from the vault's own, with age and jq.
func TestRecoverySplit(t *testing.T) {
	python, ageCmd, jqCmd, sh := tool(t, "python3"), tool(t, "age"), tool(t, "jq"), tool(t, "sh")
	d, dirs := newDispersed(t)
	if _, err := Create(d, "/", pw, Options{}); err != nil {
		t.Fatal(err)
	}
	v, _ := Unlock(d, "/", pw)
	want := fillVault(t, v.FS())
	key, _ := v.RecoveryKey()

	text, err := os.ReadFile(filepath.Join(dirs[0], recoveryFile))
	if err != nil {
		t.Fatal(err)
	}
	s := string(text)
	script := s[strings.Index(s, "---- reassemble.py ----\n")+len("---- reassemble.py ----\n") : strings.Index(s, "---- end ----")]
	scriptFile := filepath.Join(t.TempDir(), "reassemble.py")
	os.WriteFile(scriptFile, []byte(script), 0o644)

	for missing := range 3 {
		args := []string{scriptFile, dirs[0], dirs[1], dirs[2]}
		args[missing+1] = "-"
		out := filepath.Join(t.TempDir(), "vault")
		if o, err := exec.Command(python, append(args, out)...).CombinedOutput(); err != nil {
			t.Fatalf("reassemble without part %d: %v\n%s", missing+1, err, o)
		}
		os.WriteFile(filepath.Join(out, "key.txt"), []byte(key+"\n"), 0o600)
		restored := filepath.Join(t.TempDir(), "recovered")
		rs := recoveryScript(t, out, "KEY=key.txt", "restore . ../recovered", map[string]string{"age": ageCmd, "jq": jqCmd})
		rs = strings.Replace(rs, "restore . ../recovered", "restore . '"+restored+"'", 1)
		runScript(t, sh, out, rs)
		if got := treeOf(t, restored); !equalTrees(got, want) {
			t.Fatalf("without part %d: recovered tree differs: %v", missing+1, keys(got))
		}
	}
}
