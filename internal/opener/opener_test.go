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

package opener

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMimeType(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":  "image/jpeg",
		"report.pdf": "application/pdf",
		"noext":      "application/octet-stream",
	}
	for name, want := range cases {
		got := MimeType(name)
		if got != want {
			t.Errorf("MimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExpandExec(t *testing.T) {
	cases := []struct {
		exec, target string
		want         []string
	}{
		{"gedit %U", "/tmp/a.txt", []string{"gedit", "/tmp/a.txt"}},
		{"eog %f", "/tmp/b.png", []string{"eog", "/tmp/b.png"}},
		{"myapp --flag %f --other", "/tmp/c", []string{"myapp", "--flag", "/tmp/c", "--other"}},
		{"noargsapp", "/tmp/d", []string{"noargsapp", "/tmp/d"}},
		{"withicon %i %c %k app", "/tmp/e", []string{"withicon", "app", "/tmp/e"}},
	}
	for _, c := range cases {
		got := expandExec(c.exec, c.target)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandExec(%q, %q) = %#v, want %#v", c.exec, c.target, got, c.want)
		}
	}
}

func TestParseDesktopFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Test Editor\nExec=testeditor %U\nTerminal=false\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	app, ok := parseDesktopFile(path)
	if !ok {
		t.Fatal("parseDesktopFile returned ok=false")
	}
	if app.Name != "Test Editor" || app.Exec != "testeditor %U" || app.Terminal {
		t.Errorf("parsed app = %#v", app)
	}
}

func TestParseDesktopFileHiddenIsSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidden.desktop")
	content := "[Desktop Entry]\nName=Hidden App\nExec=hiddenapp\nNoDisplay=true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := parseDesktopFile(path); ok {
		t.Error("NoDisplay=true entry should not be returned")
	}
}

func TestLookupAssociation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mimeapps.list")
	content := "[Default Applications]\nimage/png=eog.desktop;gimp.desktop;\ntext/plain=gedit.desktop\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id, ok := lookupAssociation(path, "Default Applications", "image/png")
	if !ok || id != "eog.desktop" {
		t.Errorf("lookupAssociation image/png = %q, %v", id, ok)
	}
	id, ok = lookupAssociation(path, "Default Applications", "text/plain")
	if !ok || id != "gedit.desktop" {
		t.Errorf("lookupAssociation text/plain = %q, %v", id, ok)
	}
	if _, ok := lookupAssociation(path, "Default Applications", "video/mp4"); ok {
		t.Error("lookup for an absent mime type should fail")
	}
	if _, ok := lookupAssociation(path, "Added Associations", "image/png"); ok {
		t.Error("lookup targeting a section the file doesn't have should fail")
	}
}

func TestLookupAssociationAddedAssociationsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mimeapps.list")
	content := "[Default Applications]\n[Added Associations]\ntext/plain=org.gnome.gedit.desktop;libreoffice-writer.desktop;\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id, ok := lookupAssociation(path, "Added Associations", "text/plain")
	if !ok || id != "org.gnome.gedit.desktop" {
		t.Errorf("lookupAssociation text/plain = %q, %v", id, ok)
	}
	if _, ok := lookupAssociation(path, "Default Applications", "text/plain"); ok {
		t.Error("an entry under [Added Associations] must not be found under [Default Applications]")
	}
}

// TestDefaultAppPrefersAddedAssociationOverMimeCache reproduces the
// real-world scenario that motivated the three-pass lookup in DefaultApp:
// no [Default Applications] entry anywhere, but the user's own
// mimeapps.list has a [Added Associations] entry (from a plain "Open
// With…", not "always use this application") for one app, while a
// system-wide mimeinfo.cache lists a different, unrelated app as merely
// capable of opening the same type. The Added Associations entry — the
// closer approximation of "what the user actually uses" — must win.
func TestDefaultAppPrefersAddedAssociationOverMimeCache(t *testing.T) {
	configHome := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CONFIG_DIRS", t.TempDir())
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())

	mimeapps := "[Default Applications]\n[Added Associations]\ntext/plain=org.example.editor.desktop;\n"
	if err := os.WriteFile(filepath.Join(configHome, "mimeapps.list"), []byte(mimeapps), 0o644); err != nil {
		t.Fatal(err)
	}

	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	editor := "[Desktop Entry]\nName=Example Editor\nExec=example-editor %f\n"
	if err := os.WriteFile(filepath.Join(appsDir, "org.example.editor.desktop"), []byte(editor), 0o644); err != nil {
		t.Fatal(err)
	}
	suite := "[Desktop Entry]\nName=Example Office Suite\nExec=example-office %f\n"
	if err := os.WriteFile(filepath.Join(appsDir, "org.example.office.desktop"), []byte(suite), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := "[MIME Cache]\ntext/plain=org.example.office.desktop;\n"
	if err := os.WriteFile(filepath.Join(appsDir, "mimeinfo.cache"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}

	app, ok := DefaultApp("text/plain")
	if !ok {
		t.Fatal("DefaultApp returned ok=false")
	}
	if app.Name != "Example Editor" {
		t.Errorf("DefaultApp(text/plain) = %q, want %q", app.Name, "Example Editor")
	}
}

func TestUpsertAssociationAddsSectionAndKey(t *testing.T) {
	lines := upsertAssociation([]string{""}, "text/markdown", "typora.desktop")
	joined := joinForTest(lines)
	if want := "[Default Applications]\ntext/markdown=typora.desktop;"; joined != want {
		t.Errorf("got:\n%s\nwant:\n%s", joined, want)
	}
}

func TestUpsertAssociationReplacesExisting(t *testing.T) {
	lines := []string{"[Default Applications]", "text/markdown=old.desktop;"}
	lines = upsertAssociation(lines, "text/markdown", "new.desktop")
	joined := joinForTest(lines)
	if want := "[Default Applications]\ntext/markdown=new.desktop;"; joined != want {
		t.Errorf("got:\n%s\nwant:\n%s", joined, want)
	}
}

func TestUpsertAssociationAddsKeyToExistingSection(t *testing.T) {
	lines := []string{"[Default Applications]", "text/plain=gedit.desktop;", "[Added Associations]"}
	lines = upsertAssociation(lines, "image/png", "eog.desktop")
	joined := joinForTest(lines)
	want := "[Default Applications]\ntext/plain=gedit.desktop;\nimage/png=eog.desktop;\n[Added Associations]"
	if joined != want {
		t.Errorf("got:\n%s\nwant:\n%s", joined, want)
	}
}

func joinForTest(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
