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

package extract

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTextTxt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestTextUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Text(path); err == nil {
		t.Error("expected an error for an unsupported extension")
	}
}

func TestSupported(t *testing.T) {
	// .txt/.md/.pdf/.docx work with no external tools at all; .png/.mp4/""
	// never work regardless of what's installed. .doc/.rtf/.odt depend on
	// whatever pandoc/LibreOffice this machine actually has (see
	// external.go) — externalSupported is the same function Supported
	// itself calls, so these assertions track reality on any machine
	// rather than assuming a fixed answer.
	cases := map[string]bool{
		".txt": true, ".TXT": true, ".md": true, ".pdf": true, ".docx": true,
		"a.zip": true, "a.tar": true, "a.tar.gz": true, "a.TGZ": true, "a.gz": true,
		"a.bz2": true, "a.tar.bz2": true, "a.tbz2": true,
		".png": false, ".mp4": false, "": false,
		".doc":     externalSupported(".doc"),
		".rtf":     externalSupported(".rtf"),
		".odt":     externalSupported(".odt"),
		"a.xz":     archiveKindAvailable("xz"),
		"a.lzma":   archiveKindAvailable("lzma"),
		"a.tar.xz": archiveKindAvailable("tarxz"),
		"a.7z":     archiveKindAvailable("7z"),
	}
	for name, want := range cases {
		if got := Supported(name); got != want {
			t.Errorf("Supported(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestArchiveKind(t *testing.T) {
	cases := map[string]string{
		"a.zip": "zip", "a.ZIP": "zip",
		"a.tar":    "tar",
		"a.tar.gz": "targz", "a.tgz": "targz", "a.TAR.GZ": "targz",
		"a.gz":      "gzip",
		"a.bz2":     "bzip2",
		"a.tar.bz2": "tarbz2", "a.tbz2": "tarbz2", "a.tbz": "tarbz2",
		"a.xz":     "xz",
		"a.lzma":   "lzma",
		"a.tar.xz": "tarxz", "a.txz": "tarxz",
		"a.7z":  "7z",
		"a.txt": "", "a.docx": "", "": "",
	}
	for name, want := range cases {
		if got := archiveKind(name); got != want {
			t.Errorf("archiveKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestZipText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	writeZipEntry(t, zw, "a.txt", "First document about pelicans.")
	writeZipEntry(t, zw, "sub/b.txt", "Second document about penguins.")
	writeZipEntry(t, zw, "image.png", "\x89PNG binary junk, not extractable")
	writeZipEntry(t, zw, "nested.zip", "pretend nested archive bytes")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "pelicans") || !strings.Contains(got, "penguins") {
		t.Errorf("missing expected content from supported entries: %q", got)
	}
	if strings.Contains(got, "PNG binary junk") || strings.Contains(got, "nested archive bytes") {
		t.Errorf("unsupported/nested-archive entries should have been skipped: %q", got)
	}
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name, content string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

func TestTarText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTar(t, f, map[string]string{
		"a.txt": "A document about volcanoes.",
		"b.txt": "A document about glaciers.",
		"c.bin": "unsupported binary content",
	})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "volcanoes") || !strings.Contains(got, "glaciers") {
		t.Errorf("missing expected content: %q", got)
	}
	if strings.Contains(got, "unsupported binary") {
		t.Errorf("unsupported entry should have been skipped: %q", got)
	}
}

func TestTarGzText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	writeTar(t, gz, map[string]string{"a.txt": "A document about coral reefs."})
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "coral reefs") {
		t.Errorf("missing expected content: %q", got)
	}
}

// writeTar writes files (name -> content) as a tar stream to w, in
// alphabetical order for deterministic test output.
func writeTar(t *testing.T, w io.Writer, files map[string]string) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tar.NewWriter(w)
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGzipText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Name = "report.txt"
	if _, err := gz.Write([]byte("A single compressed document about tectonic plates.")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "tectonic plates") {
		t.Errorf("missing expected content: %q", got)
	}
}

// TestBzip2Text exercises bzip2Text's *decompression* (the only bzip2
// operation shfm's production code ever does — compress/bzip2 in the Go
// standard library is read-only, matching what runtime extraction needs)
// against a real .bz2 file. There being no pure-Go bzip2 encoder anywhere
// in play, the fixture itself is produced by shelling out to the bzip2
// command; if that's not installed, there's no way to build the fixture at
// all, so the test skips — this is purely a test-setup convenience, not a
// new runtime dependency of the code under test.
func TestBzip2Text(t *testing.T) {
	bzip2Cmd, err := exec.LookPath("bzip2")
	if err != nil {
		t.Skip("bzip2 command not found on this machine (needed to build the test fixture)")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt.bz2")
	writeViaCommand(t, bzip2Cmd, path, []string{"-zc"}, "A single bzip2-compressed document about desert ecosystems.")

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "desert ecosystems") {
		t.Errorf("missing expected content: %q", got)
	}
}

func TestTarBz2Text(t *testing.T) {
	bzip2Cmd, err := exec.LookPath("bzip2")
	if err != nil {
		t.Skip("bzip2 command not found on this machine (needed to build the test fixture)")
	}
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "docs.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTar(t, f, map[string]string{"a.txt": "A document about arctic wildlife."})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "docs.tar.bz2")
	cmd := exec.Command(bzip2Cmd, "-zc", tarPath)
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "arctic wildlife") {
		t.Errorf("missing expected content: %q", got)
	}
}

// TestXzText exercises the xz command-backed path end-to-end — skipped if
// xz isn't installed, since compress/bzip2-style pure-Go writing isn't an
// option here (the stdlib has no xz/lzma encoder either, only a decoder is
// needed at runtime, and this test needs to *produce* a real .xz file).
func TestXzText(t *testing.T) {
	if !archiveKindAvailable("xz") {
		t.Skip("xz not found on this machine")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt.xz")
	writeViaCommand(t, xzPath, path, []string{"-zc"}, "A single xz-compressed document about tidal energy.")

	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "tidal energy") {
		t.Errorf("missing expected content: %q", got)
	}
}

func TestTarXzText(t *testing.T) {
	if !archiveKindAvailable("tarxz") {
		t.Skip("xz not found on this machine")
	}
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "docs.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTar(t, f, map[string]string{"a.txt": "A document about wind turbine maintenance."})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "docs.tar.xz")
	cmd := exec.Command(xzPath, "-zc", tarPath)
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Text(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "wind turbine") {
		t.Errorf("missing expected content: %q", got)
	}
}

// TestSevenZText exercises the 7z command-backed path end-to-end — skipped
// if no 7z/7zz/7za build is installed.
func TestSevenZText(t *testing.T) {
	if !archiveKindAvailable("7z") {
		t.Skip("no 7z/7zz/7za found on this machine")
	}
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("A document about beekeeping."), 0o644); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "docs.7z")
	cmd := exec.Command(sevenZipPath, "a", archivePath, filepath.Join(srcDir, "a.txt"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("7z a failed: %v: %s", err, out)
	}

	got, err := Text(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "beekeeping") {
		t.Errorf("missing expected content: %q", got)
	}
}

// writeViaCommand runs name (a compressor binary) with args plus "-c" to
// stdout, feeding content on its stdin, and writes the result to path — a
// small helper so tests that only have a decoder available in Go (xz) can
// still produce a real compressed fixture.
func writeViaCommand(t *testing.T, name, path string, args []string, content string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(content)
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

// TestTexPlainFallback exercises plainText directly, the always-available
// fallback for .tex (see Text's dispatch) — going through Text here would
// make the test depend on whether pandoc happens to be installed, for
// something that's specifically testing the no-external-tools path.
func TestTexPlainFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "paper.tex")
	tex := `\section{Introduction}\nThis paper discusses quantum entanglement.`
	if err := os.WriteFile(path, []byte(tex), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := plainText(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != tex {
		t.Errorf("got %q, want %q (plainText must be verbatim, no stripping)", got, tex)
	}
}

// TestTexPandoc exercises the pandoc-backed .tex path end-to-end — skipped
// if pandoc isn't installed. Unlike plainText's verbatim passthrough,
// pandoc's LaTeX reader turns markup into actual prose, so the raw
// "\section{...}" command should NOT survive into the extracted text.
func TestTexPandoc(t *testing.T) {
	locateExternalTools()
	if pandocPath == "" {
		t.Skip("pandoc not found on this machine")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "paper.tex")
	tex := "\\section{Introduction}\nThis paper discusses quantum entanglement.\n"
	if err := os.WriteFile(path, []byte(tex), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "quantum entanglement") {
		t.Errorf("missing expected content: %q", got)
	}
	if strings.Contains(got, `\section{`) {
		t.Errorf("raw LaTeX markup leaked through pandoc's reader: %q", got)
	}
}

// TestSofficeOnlyNoOutputLeak forces the LibreOffice-only branch of
// extractExternal (by temporarily hiding pandoc, regardless of whether
// it's actually installed on this machine) and checks the child process's
// own stdout/stderr never touch shfm's — every command this package runs
// must have Stdout/Stderr explicitly set to something Go captures or
// discards, since shfm is a full-screen TUI holding the real terminal: raw
// output from an external tool landing there corrupts the display (see
// redirectLlamaLogging's doc comment in engine_enabled.go for the exact
// same failure mode with llama.cpp's own logging).
//
// This can't be checked by redirecting the os.Stdout variable and looking
// for leakage there — cmd.Stdout is set to a Go-level io.Writer (a buffer
// or io.Discard), so the child's output never touches file descriptor 1 in
// the first place regardless of what os.Stdout currently points to. What
// this test actually verifies is that the conversion completes correctly
// end-to-end through that branch — combined with the explicit
// `cmd.Stdout = io.Discard` in runSofficeConvert (see external.go), that's
// what rules out a leak.
func TestSofficeOnlyNoOutputLeak(t *testing.T) {
	locateExternalTools()
	if sofficePath == "" {
		t.Skip("LibreOffice not found on this machine")
	}
	savedPandoc := pandocPath
	pandocPath = ""
	defer func() { pandocPath = savedPandoc }()

	dir := t.TempDir()
	path := filepath.Join(dir, "note.rtf")
	rtf := `{\rtf1\ansi\deff0 Checking the LibreOffice-only path leaves our own stdout alone.}`
	if err := os.WriteFile(path, []byte(rtf), 0o644); err != nil {
		t.Fatal(err)
	}

	text, ok, err := extractExternal(path)
	if !ok {
		t.Fatal("expected extractExternal to handle .rtf via LibreOffice with pandoc hidden")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "leaves our own stdout alone") {
		t.Errorf("unexpected extracted text: %q", text)
	}
}

// TestExternalRTF exercises the pandoc/LibreOffice path end-to-end against
// a real (hand-written, since RTF is plain text) file — skipped if neither
// tool is installed, since there's nothing to test in that case.
func TestExternalRTF(t *testing.T) {
	if !externalSupported(".rtf") {
		t.Skip("neither pandoc nor LibreOffice found on this machine")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "note.rtf")
	rtf := `{\rtf1\ansi\deff0 Hello from a test RTF document.}`
	if err := os.WriteFile(path, []byte(rtf), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Text(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Hello from a test RTF document.") {
		t.Errorf("extracted text missing expected content: %q", got)
	}
}

// TestDocxText exercises docxText directly, not the tool-aware Text
// dispatcher: the minimal fixture below is a bare word/document.xml with
// none of the other parts ([Content_Types].xml, _rels/, ...) a real DOCX
// package carries, which this package's own simplified reader tolerates
// but a real tool like LibreOffice or pandoc correctly refuses to load —
// going through Text here would make this test depend on whether external
// tools happen to be installed, for something that's specifically testing
// this package's own fallback parser.
func TestDocxText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.docx")
	writeMinimalDocx(t, path, []string{"First paragraph.", "Second paragraph."})

	got, err := docxText(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "First paragraph.") || !strings.Contains(got, "Second paragraph.") {
		t.Errorf("extracted text missing expected paragraphs: %q", got)
	}
}

// writeMinimalDocx builds a bare-bones but structurally valid .docx (a zip
// with just word/document.xml) with one paragraph per string in paragraphs,
// each split across two runs to also exercise multi-run concatenation.
func writeMinimalDocx(t *testing.T, path string, paragraphs []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="ns"><w:body>`)
	for _, p := range paragraphs {
		half := len(p) / 2
		b.WriteString(`<w:p><w:r><w:t>` + p[:half] + `</w:t></w:r><w:r><w:t>` + p[half:] + `</w:t></w:r></w:p>`)
	}
	b.WriteString(`</w:body></w:document>`)
	if _, err := w.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// Goes through docxText directly for the same reason as TestDocxText above
// — notably, Text would not even reliably fail here: LibreOffice's format
// auto-detection is lenient enough to accept these arbitrary bytes as plain
// text and "succeed", which is correct behaviour for that tool but not what
// this test is checking.
func TestDocxTextNotADocx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.docx")
	if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := docxText(path); err == nil {
		t.Error("expected an error for a .docx that isn't actually a zip")
	}
}
