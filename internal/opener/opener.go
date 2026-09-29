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

// Package opener resolves, launches and manages the applications that
// open each file type, following the freedesktop.org specifications GNOME,
// KDE and the other Linux desktops share, directly: Shared MIME-info (a
// file's type, mimeinfo.go), Desktop Entry (the installed applications,
// desktop.go) and MIME Applications Associations (which of them opens a
// type, and changing it, assoc.go) — no dependency on xdg-open, xdg-mime
// or any other external command. Actually launching the chosen
// application is, necessarily, done via os/exec (that's simply how you run
// another program), exactly as any file manager does.
package opener

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"shfm/internal/termlaunch"
	"shfm/internal/toolpath"
)

// App describes an installed application discovered from a .desktop file.
type App struct {
	ID       string // desktop file ID, e.g. "org.gnome.eog.desktop" ("kde4-foo.desktop" for kde4/foo.desktop)
	Name     string // in the user's language, when the entry has it
	Exec     string // raw Exec= value, with %f/%u/... placeholders still in place
	Terminal bool
	Icon     string
	WorkDir  string // Path=: the folder to run it in, "" for any
	File     string // the .desktop file's path

	// MimeTypes are the types the app declares it can open (MimeType=).
	MimeTypes []string

	NoDisplay             bool     // installed, but not for menus or choosers
	OnlyShowIn, NotShowIn []string // desktops to show it in, or not

	dir int // index in appDirs of the directory it's installed in
}

// TerminalCommand is the terminal emulator to run Terminal=true
// applications in (config.json's "terminal"; see termlaunch.Command).
var TerminalCommand string

// configDirs are the XDG base config directories, highest priority (the
// user's) first.
func configDirs() []string {
	var dirs []string
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		dirs = append(dirs, configHome)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config"))
	}
	if configDirsEnv := os.Getenv("XDG_CONFIG_DIRS"); configDirsEnv != "" {
		for _, d := range strings.Split(configDirsEnv, ":") {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	} else {
		dirs = append(dirs, "/etc/xdg")
	}
	return dirs
}

// DefaultApp returns the application that opens mimeType, as the MIME
// Applications Associations specification resolves it (see
// assocIndex.resolve).
func DefaultApp(mimeType string) (App, bool) {
	return loadAssocIndex().resolve(mimeType)
}

// expandExec turns a .desktop Exec= value into argv for opening
// targetPath, by the Desktop Entry spec's rules: arguments split on
// spaces, double quotes grouping one (with \" \` \$ \\ escaped inside),
// and the field codes expanded — %f %F %u %U to the file (a path is valid
// for a URL code), %i to "--icon <Icon>", %c to the name, %k to the
// .desktop file, %% to "%", the deprecated ones dropped. An Exec with no
// file code gets the file appended, as other desktops do.
func expandExec(app App, targetPath string) ([]string, error) {
	var out []string
	substituted := false
	line := app.Exec
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		var arg strings.Builder
		quoted := false
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			if line[i] != '"' {
				arg.WriteByte(line[i])
				i++
				continue
			}
			quoted = true
			i++
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("\"`$\\", line[i+1]) >= 0 {
					i++
				}
				arg.WriteByte(line[i])
				i++
			}
			if i == len(line) {
				return nil, fmt.Errorf("unterminated quote in Exec=%s", app.Exec)
			}
			i++ // closing quote
		}
		raw := arg.String()
		if !quoted && i-start == 2 {
			switch raw {
			case "%F", "%U":
				out = append(out, targetPath)
				substituted = true
				continue
			case "%i":
				if app.Icon != "" {
					out = append(out, "--icon", app.Icon)
				}
				continue
			}
		}
		// Field codes within an argument; quoted ones only ever hold %%.
		var b strings.Builder
		for j := 0; j < len(raw); j++ {
			if raw[j] != '%' || j+1 == len(raw) {
				b.WriteByte(raw[j])
				continue
			}
			j++
			switch raw[j] {
			case '%':
				b.WriteByte('%')
			case 'f', 'u', 'F', 'U':
				if quoted {
					b.WriteByte('%')
					b.WriteByte(raw[j])
					continue
				}
				b.WriteString(targetPath)
				substituted = true
			case 'c':
				b.WriteString(app.Name)
			case 'k':
				b.WriteString(app.File)
			case 'i', 'd', 'D', 'n', 'N', 'v', 'm':
				// %i only stands alone; the others are deprecated.
			default:
				b.WriteByte('%')
				b.WriteByte(raw[j])
			}
		}
		if b.Len() > 0 || quoted {
			out = append(out, b.String())
		}
	}
	if !substituted {
		out = append(out, targetPath)
	}
	return out, nil
}

// buildCmd prepares (without starting) the command to launch app on
// targetPath, detached from shfm's own process group/terminal — in a new
// terminal window for a Terminal=true app.
func buildCmd(app App, targetPath string) (*exec.Cmd, error) {
	argv, err := expandExec(app, targetPath)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty Exec= line for %s", app.Name)
	}
	// Found where PATH alone may miss it (see toolpath); otherwise left
	// for exec to report as not found.
	if p, err := toolpath.Find(argv[0]); err == nil {
		argv[0] = p
	}
	if app.Terminal {
		if argv, err = termlaunch.Command(TerminalCommand, argv); err != nil {
			return nil, fmt.Errorf("%s runs in a terminal: %w", app.Name, err)
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = app.WorkDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, nil
}

// Launch starts app with targetPath as its argument, detached from shfm
// (its own process group, not waited on), so shfm keeps running normally.
func Launch(app App, targetPath string) error {
	cmd, err := buildCmd(app, targetPath)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not launch %s: %w", app.Name, err)
	}
	go cmd.Wait() // reap the child without blocking shfm
	return nil
}

// LaunchAndWait is like Launch, but blocks the calling goroutine until app
// exits — for callers that need to react afterwards, e.g. syncing a local
// temp copy of a remote file back to its source once the user is done
// editing it. This is a best-effort signal for "done editing": an app that
// hands off to an already-running instance (common for single-instance GUI
// apps, and terminals) may exit immediately, well before the user actually
// closes the document in that other instance.
func LaunchAndWait(app App, targetPath string) error {
	cmd, err := buildCmd(app, targetPath)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not launch %s: %w", app.Name, err)
	}
	return cmd.Wait()
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}
