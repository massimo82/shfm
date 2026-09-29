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
	"strings"
	"testing"
)

// testEnv sandboxes every XDG directory opener reads (left unset, they'd
// fall back to the machine's real ones), with a small Shared MIME-info
// database, and returns the user's config and data homes.
func testEnv(t *testing.T) (configHome, dataHome string) {
	t.Helper()
	configHome, dataHome = t.TempDir(), t.TempDir()
	sysData := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CONFIG_DIRS", t.TempDir())
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_DATA_DIRS", sysData)
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "C")
	writeFile(t, filepath.Join(sysData, "mime", "globs2"), strings.Join([]string{
		"# comment",
		"50:text/plain:*.txt",
		"50:text/x-csrc:*.c",
		"50:text/x-c++src:*.C:cs",
		"50:text/markdown:*.md",
		"50:image/jpeg:*.jpg",
		"50:image/jpeg:*.jpeg",
		"50:image/png:*.png",
		"50:application/pdf:*.pdf",
		"50:application/gzip:*.gz",
		"55:application/x-compressed-tar:*.tar.gz",
		"50:text/x-makefile:Makefile",
		"50:text/x-readme:README*",
	}, "\n"))
	writeFile(t, filepath.Join(sysData, "mime", "aliases"), "application/x-pdf application/pdf\n")
	writeFile(t, filepath.Join(sysData, "mime", "subclasses"), "text/x-csrc text/plain\n")
	return configHome, dataHome
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeApp(t *testing.T, dataHome, id, body string) {
	t.Helper()
	writeFile(t, filepath.Join(dataHome, "applications", id), "[Desktop Entry]\nType=Application\n"+body)
}

func TestMimeType(t *testing.T) {
	testEnv(t)
	cases := map[string]string{
		"photo.jpg":    "image/jpeg",
		"PHOTO.JPG":    "image/jpeg", // case-insensitive glob
		"main.c":       "text/x-csrc",
		"main.C":       "text/x-c++src", // case-sensitive glob wins over *.c
		"a.tar.gz":     "application/x-compressed-tar",
		"a.gz":         "application/gzip",
		"Makefile":     "text/x-makefile", // literal
		"README.first": "text/x-readme",   // glob
		"report.pdf":   "application/pdf",
		"noext":        "application/octet-stream",
	}
	for name, want := range cases {
		if got := MimeType(name); got != want {
			t.Errorf("MimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtensionsAndParseMimeInput(t *testing.T) {
	testEnv(t)
	if got := Extensions("image/jpeg"); !reflect.DeepEqual(got, []string{".jpg", ".jpeg"}) {
		t.Errorf("Extensions(image/jpeg) = %v", got)
	}
	if got := Extensions("application/x-pdf"); !reflect.DeepEqual(got, []string{".pdf"}) {
		t.Errorf("Extensions of an alias = %v", got)
	}
	for in, want := range map[string]string{
		"pdf": "application/pdf", ".md": "text/markdown", "x.png": "image/png",
		"application/x-pdf": "application/pdf", "Image/PNG": "image/png",
	} {
		if got, err := ParseMimeInput(in); err != nil || got != want {
			t.Errorf("ParseMimeInput(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "zzqq", "image/", "a b/c"} {
		if _, err := ParseMimeInput(in); err == nil {
			t.Errorf("ParseMimeInput(%q) should fail", in)
		}
	}
}

func TestExpandExec(t *testing.T) {
	app := App{Name: "My App", Icon: "myicon", File: "/apps/my.desktop"}
	cases := []struct {
		exec string
		want []string
	}{
		{"gedit %U", []string{"gedit", "/tmp/a b.txt"}},
		{"eog %f", []string{"eog", "/tmp/a b.txt"}},
		{"myapp --flag %f --other", []string{"myapp", "--flag", "/tmp/a b.txt", "--other"}},
		{"noargsapp", []string{"noargsapp", "/tmp/a b.txt"}},
		{"withicon %i %c %k app", []string{"withicon", "--icon", "myicon", "My App", "/apps/my.desktop", "app", "/tmp/a b.txt"}},
		{`sh -c "echo \"hi\" \$HOME 100%%" %u`, []string{"sh", "-c", `echo "hi" $HOME 100%`, "/tmp/a b.txt"}},
		{`"/opt/My App/bin/app" --open=%u`, []string{"/opt/My App/bin/app", "--open=/tmp/a b.txt"}},
		{"old %d %n %f", []string{"old", "/tmp/a b.txt"}},
	}
	for _, c := range cases {
		app.Exec = c.exec
		got, err := expandExec(app, "/tmp/a b.txt")
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandExec(%q) = %#v, %v; want %#v", c.exec, got, err, c.want)
		}
	}
	app.Exec = `app "unterminated %f`
	if _, err := expandExec(app, "/x"); err == nil {
		t.Error("an unterminated quote should fail")
	}
}

func TestParseDesktopFile(t *testing.T) {
	t.Setenv("LC_ALL", "it_IT.UTF-8")
	dir := t.TempDir()
	path := filepath.Join(dir, "test.desktop")
	writeFile(t, path, "[Desktop Entry]\nType=Application\nName=Test Editor\nName[it]=Editor di prova\n"+
		"Exec=testeditor %U\nTerminal=false\nMimeType=text/plain;text/x-a\\;b;\nPath=/srv\n"+
		"[Desktop Action new]\nName=Other\nExec=other\n")
	app, ok := parseDesktopFile(path)
	if !ok {
		t.Fatal("parseDesktopFile returned ok=false")
	}
	if app.Name != "Editor di prova" || app.Exec != "testeditor %U" || app.Terminal || app.WorkDir != "/srv" {
		t.Errorf("parsed app = %#v", app)
	}
	if !reflect.DeepEqual(app.MimeTypes, []string{"text/plain", "text/x-a;b"}) {
		t.Errorf("MimeTypes = %#v", app.MimeTypes)
	}
}

func TestParseDesktopFileRejects(t *testing.T) {
	for name, body := range map[string]string{
		"hidden":  "Type=Application\nName=A\nExec=a\nHidden=true\n",
		"link":    "Type=Link\nName=A\nURL=https://example.org\n",
		"noexec":  "Type=Application\nName=A\n",
		"tryexec": "Type=Application\nName=A\nExec=a\nTryExec=/nonexistent/shfm-test-binary\n",
	} {
		path := filepath.Join(t.TempDir(), "a.desktop")
		writeFile(t, path, "[Desktop Entry]\n"+body)
		if _, ok := parseDesktopFile(path); ok {
			t.Errorf("%s: entry should be rejected", name)
		}
	}
}

func TestListAppsFollowsSpec(t *testing.T) {
	_, dataHome := testEnv(t)
	t.Setenv("XDG_CURRENT_DESKTOP", "KDE")
	sysApps := filepath.Join(os.Getenv("XDG_DATA_DIRS"), "applications")
	writeApp(t, dataHome, "sub/nested.desktop", "Name=Nested\nExec=nested\n")
	writeApp(t, dataHome, "nodisplay.desktop", "Name=Helper\nExec=helper\nNoDisplay=true\n")
	writeApp(t, dataHome, "gnomeonly.desktop", "Name=Gnome Only\nExec=g\nOnlyShowIn=GNOME;\n")
	writeApp(t, dataHome, "notkde.desktop", "Name=Not KDE\nExec=n\nNotShowIn=KDE;\n")
	// A user's Hidden entry deletes the system one with the same ID.
	writeApp(t, dataHome, "gone.desktop", "Name=Gone\nExec=gone\nHidden=true\n")
	writeFile(t, filepath.Join(sysApps, "gone.desktop"), "[Desktop Entry]\nType=Application\nName=Gone\nExec=gone\n")
	writeFile(t, filepath.Join(sysApps, "kept.desktop"), "[Desktop Entry]\nType=Application\nName=Kept\nExec=kept\n")

	var names []string
	for _, a := range ListApps() {
		names = append(names, a.ID)
	}
	if want := []string{"kept.desktop", "sub-nested.desktop"}; !reflect.DeepEqual(names, want) {
		t.Errorf("ListApps IDs = %v, want %v", names, want)
	}
	if _, ok := installedApps()["nodisplay.desktop"]; !ok {
		t.Error("a NoDisplay app is still installed, valid as a default")
	}
}

func TestLoadIni(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mimeapps.list")
	writeFile(t, path, "[Default Applications]\nimage/png=eog.desktop;gimp.desktop;\ntext/plain = gedit.desktop\n"+
		"[Added Associations]\ntext/plain=org.gnome.gedit.desktop;libreoffice-writer.desktop;\n")
	ini := loadIni(path, nil)
	if got := ini[sectionDefault]["image/png"]; !reflect.DeepEqual(got, []string{"eog.desktop", "gimp.desktop"}) {
		t.Errorf("image/png = %v", got)
	}
	if got := ini[sectionDefault]["text/plain"]; !reflect.DeepEqual(got, []string{"gedit.desktop"}) {
		t.Errorf("text/plain = %v", got)
	}
	if got := ini[sectionAdded]["text/plain"]; len(got) != 2 {
		t.Errorf("added text/plain = %v", got)
	}
	if _, ok := ini[sectionDefault]["video/mp4"]; ok {
		t.Error("lookup for an absent mime type should fail")
	}
}

// TestDefaultAppPrefersAddedAssociation: with no default anywhere, the
// user's [Added Associations] (a plain "Open With…") wins over an app
// merely declaring it can open the type.
func TestDefaultAppPrefersAddedAssociation(t *testing.T) {
	configHome, dataHome := testEnv(t)
	writeFile(t, filepath.Join(configHome, "mimeapps.list"), "[Default Applications]\n[Added Associations]\ntext/plain=org.example.editor.desktop;\n")
	writeApp(t, dataHome, "org.example.editor.desktop", "Name=Example Editor\nExec=example-editor %f\n")
	writeApp(t, dataHome, "org.example.office.desktop", "Name=Example Office Suite\nExec=example-office %f\nMimeType=text/plain;\n")

	app, ok := DefaultApp("text/plain")
	if !ok || app.Name != "Example Editor" {
		t.Errorf("DefaultApp(text/plain) = %q, %v; want Example Editor", app.Name, ok)
	}
}

func TestDefaultAppResolution(t *testing.T) {
	configHome, dataHome := testEnv(t)
	t.Setenv("XDG_CURRENT_DESKTOP", "ubuntu:GNOME")
	sysConfig := os.Getenv("XDG_CONFIG_DIRS")
	for id, types := range map[string]string{"a": "", "b": "image/png", "c": "image/png", "d": "", "e": "text/plain"} {
		writeApp(t, dataHome, id+".desktop", "Name="+strings.ToUpper(id)+"\nExec="+id+"\nMimeType="+types+";\n")
	}
	// The desktop-specific list beats mimeapps.list; an uninstalled
	// default is skipped for the next one.
	writeFile(t, filepath.Join(sysConfig, "mimeapps.list"), "[Default Applications]\napplication/pdf=a.desktop;\n")
	writeFile(t, filepath.Join(sysConfig, "gnome-mimeapps.list"), "[Default Applications]\napplication/pdf=missing.desktop;d.desktop;\n")
	// The user removes b: the next app declaring the type is used.
	writeFile(t, filepath.Join(configHome, "mimeapps.list"), "[Removed Associations]\nimage/png=b.desktop;\n"+
		"[Default Applications]\napplication/x-pdf=\n")

	for mt, want := range map[string]string{
		"application/pdf":   "D",
		"application/x-pdf": "D", // an alias
		"image/png":         "C",
		"text/x-csrc":       "E", // a subclass of text/plain
	} {
		if app, ok := DefaultApp(mt); !ok || app.Name != want {
			t.Errorf("DefaultApp(%s) = %q, %v; want %q", mt, app.Name, ok, want)
		}
	}
	if _, ok := DefaultApp("video/mp4"); ok {
		t.Error("DefaultApp(video/mp4) should find nothing")
	}
}

func TestSaveAndResetAssociation(t *testing.T) {
	configHome, dataHome := testEnv(t)
	t.Setenv("XDG_CURRENT_DESKTOP", "KDE")
	writeApp(t, dataHome, "a.desktop", "Name=A\nExec=a\nMimeType=image/png;\n")
	writeApp(t, dataHome, "b.desktop", "Name=B\nExec=b\n")
	userList := filepath.Join(configHome, "mimeapps.list")
	writeFile(t, userList, "# mine\n[Added Associations]\nimage/png=a.desktop;\n\n[Removed Associations]\nimage/png=b.desktop;x.desktop;\n")
	kdeList := filepath.Join(configHome, "kde-mimeapps.list")
	writeFile(t, kdeList, "[Default Applications]\nimage/png=a.desktop;\ntext/plain=a.desktop;\n")

	if err := SaveDefaultApp("image/png", App{ID: "b.desktop"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(userList)
	want := "# mine\n[Added Associations]\nimage/png=b.desktop;a.desktop;\n\n[Removed Associations]\nimage/png=x.desktop;\n\n[Default Applications]\nimage/png=b.desktop;\n"
	if string(got) != want {
		t.Errorf("mimeapps.list:\n%s\nwant:\n%s", got, want)
	}
	if got, _ := os.ReadFile(kdeList); string(got) != "[Default Applications]\ntext/plain=a.desktop;\n" {
		t.Errorf("kde-mimeapps.list keeps a default overriding the choice:\n%s", got)
	}
	if app, _ := DefaultApp("image/png"); app.Name != "B" {
		t.Errorf("DefaultApp after SaveDefaultApp = %q", app.Name)
	}

	var png Association
	for _, a := range Associations() {
		if a.MimeType == "image/png" {
			png = a
		}
	}
	if !png.User || png.App.Name != "B" || !reflect.DeepEqual(png.Extensions, []string{".png"}) {
		t.Errorf("Associations image/png = %+v", png)
	}

	if err := ResetAssociation("image/png"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(userList); string(got) != "# mine\n[Added Associations]\n\n[Removed Associations]\n\n[Default Applications]\n" {
		t.Errorf("after reset:\n%s", got)
	}
	if app, _ := DefaultApp("image/png"); app.Name != "A" {
		t.Errorf("after reset DefaultApp = %q, want the app declaring the type", app.Name)
	}
}

func TestSetAssociation(t *testing.T) {
	cases := []struct {
		in      string
		section string
		ids     []string
		want    string
	}{
		{"", sectionDefault, []string{"t.desktop"}, "[Default Applications]\ntext/markdown=t.desktop;"},
		{"[Default Applications]\ntext/markdown=old.desktop;", sectionDefault, []string{"new.desktop"}, "[Default Applications]\ntext/markdown=new.desktop;"},
		{"[Default Applications]\ntext/plain=g.desktop;\n\n[Added Associations]", sectionDefault, []string{"e.desktop"},
			"[Default Applications]\ntext/plain=g.desktop;\ntext/markdown=e.desktop;\n\n[Added Associations]"},
		{"[Default Applications]\ntext/markdown=old.desktop;\ntext/plain=g.desktop;", sectionDefault, nil, "[Default Applications]\ntext/plain=g.desktop;"},
		{"[Added Associations]\ntext/plain=g.desktop;", sectionDefault, nil, "[Added Associations]\ntext/plain=g.desktop;"},
	}
	for _, c := range cases {
		got := strings.Join(setAssociation(strings.Split(c.in, "\n"), c.section, "text/markdown", c.ids), "\n")
		if got != c.want {
			t.Errorf("setAssociation(%q, %v):\n%s\nwant:\n%s", c.in, c.ids, got, c.want)
		}
	}
}

// testMagic is a small magic file, in update-mime-database's format.
const testMagic = "MIME-Magic\x00\n" +
	"[80:application/pdf]\n>0=\x00\x04%PDF\n" +
	"[70:application/x-nested]\n>0=\x00\x02AB\n1>4=\x00\x02CD\n1>4=\x00\x02EF\n" +
	"[60:image/png]\n>0=\x00\x04\x89PNG\n" +
	"[40:application/x-ranged]\n>0=\x00\x03BAR+10\n" +
	"[40:application/x-masked]\n>0=\x00\x01\xe0&\xf0\n" +
	"[30:application/x-word]\n>0=\x00\x02\x12\x34~2\n" +
	"[20:application/x-future]\n>0=\x00\x02ZZ!unknown-flag\n" +
	"[10:application/x-alias-of-pdf]\n>0=\x00\x03PDX\n"

func TestDetectMimeType(t *testing.T) {
	testEnv(t)
	sys := os.Getenv("XDG_DATA_DIRS")
	writeFile(t, filepath.Join(sys, "mime", "magic"), testMagic)
	writeFile(t, filepath.Join(sys, "mime", "aliases"), "application/x-alias-of-pdf application/pdf\n")

	for content, want := range map[string]string{
		"%PDF-1.7\n":          "application/pdf",
		"ABxxCD":              "application/x-nested",
		"ABxxEF":              "application/x-nested",
		"ABxxGH\x00":          "application/octet-stream", // parent matches, no child does
		"\x89PNG\r\n\x1a\n":   "image/png",
		"\x00\x00\x00BAR\x00": "application/x-ranged",
		"\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00BAR": "application/octet-stream", // past the range
		"\xea\x00":             "application/x-masked",
		"\x34\x12\x00":         "application/x-word",       // little-endian host
		"ZZ\x00":               "application/octet-stream", // an unknown rule is skipped
		"PDX":                  "application/pdf",          // an alias
		"#!/bin/sh\necho hi\n": "text/plain",
		"caffè\tlatte\r\n":     "text/plain",
		"":                     "application/x-zerosize",
	} {
		got := DetectMimeType("noext", func(n int) ([]byte, error) {
			if n < SniffLength() {
				t.Errorf("readHead asked for %d bytes, less than %d", n, SniffLength())
			}
			return []byte(content), nil
		})
		if got != want {
			t.Errorf("DetectMimeType(%q) = %q, want %q", content, got, want)
		}
	}
	// A name that tells the type doesn't read the file.
	if got := DetectMimeType("a.png", func(int) ([]byte, error) {
		t.Error("readHead called for a known extension")
		return nil, nil
	}); got != "image/png" {
		t.Errorf("DetectMimeType(a.png) = %q", got)
	}
	if got := DetectMimeType("noext", func(int) ([]byte, error) { return nil, os.ErrPermission }); got != "application/octet-stream" {
		t.Errorf("DetectMimeType on a read error = %q", got)
	}
	if SniffLength() < 4096 {
		t.Errorf("SniffLength = %d", SniffLength())
	}
}

func TestMagicNoMagicHidesLowerDirectories(t *testing.T) {
	_, dataHome := testEnv(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_DATA_DIRS"), "mime", "magic"), testMagic)
	writeFile(t, filepath.Join(dataHome, "mime", "magic"), "MIME-Magic\x00\n[50:image/png]\n__NOMAGIC__\n")
	if got := MimeTypeOfContent([]byte("\x89PNG\r\n\x1a\n")); got != "application/octet-stream" {
		t.Errorf("with __NOMAGIC__ for image/png, got %q", got)
	}
	if got := MimeTypeOfContent([]byte("%PDF")); got != "application/pdf" {
		t.Errorf("other types' rules must stay, got %q", got)
	}
}

func TestMimeTypeIs(t *testing.T) {
	testEnv(t)
	for _, c := range []struct {
		t, pattern string
		want       bool
	}{
		{"text/x-csrc", "text/x-csrc", true},
		{"text/x-csrc", "text/plain", true},   // declared subclass
		{"text/markdown", "text/plain", true}, // every text/* is text
		{"text/x-csrc", "text/*", true},
		{"application/x-pdf", "application/pdf", true}, // alias
		{"application/pdf", "Application/X-PDF", true},
		{"image/png", "text/plain", false},
		{"image/png", "application/octet-stream", false},
	} {
		if got := MimeTypeIs(c.t, c.pattern); got != c.want {
			t.Errorf("MimeTypeIs(%s, %s) = %v, want %v", c.t, c.pattern, got, c.want)
		}
	}
}
