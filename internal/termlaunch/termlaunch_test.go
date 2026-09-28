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

package termlaunch

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

// fake makes only installed exist, at /bin/<name>, with $TERMINAL set to
// terminal.
func fake(t *testing.T, terminal string, installed ...string) {
	t.Helper()
	oldLook, oldEnv := lookPath, getenv
	t.Cleanup(func() { lookPath, getenv = oldLook, oldEnv })
	lookPath = func(name string) (string, error) {
		for _, i := range installed {
			if name == i || name == "/bin/"+i {
				return "/bin/" + i, nil
			}
		}
		return "", exec.ErrNotFound
	}
	getenv = func(key string) string {
		if key == "TERMINAL" {
			return terminal
		}
		return ""
	}
}

func TestCommand(t *testing.T) {
	args := []string{"/usr/bin/shfm", "--pick", "/run/sock"}
	for _, tc := range []struct {
		name       string
		configured string
		terminal   string
		installed  []string
		want       []string
	}{
		{"configured known", "alacritty", "", []string{"alacritty", "foot"},
			[]string{"alacritty", "-e", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"configured with args", "wezterm start --", "", []string{"wezterm"},
			[]string{"wezterm", "start", "--", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"configured unknown", "myterm", "", []string{"myterm"},
			[]string{"myterm", "-e", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"configured missing, $TERMINAL", "nope", "kitty", []string{"kitty"},
			[]string{"kitty", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"$TERMINAL as path", "", "/bin/foot", []string{"foot"},
			[]string{"/bin/foot", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"probe prefers xdg-terminal-exec", "", "", []string{"xterm", "xdg-terminal-exec"},
			[]string{"/bin/xdg-terminal-exec", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"probe order", "", "", []string{"xterm", "ghostty", "alacritty"},
			[]string{"/bin/alacritty", "-e", "/usr/bin/shfm", "--pick", "/run/sock"}},
		{"gnome-terminal", "", "", []string{"gnome-terminal"},
			[]string{"/bin/gnome-terminal", "--", "/usr/bin/shfm", "--pick", "/run/sock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake(t, tc.terminal, tc.installed...)
			got, err := Command(tc.configured, args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandNoTerminal(t *testing.T) {
	fake(t, "")
	if _, err := Command("", []string{"shfm"}); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("error = %v, want ErrNoTerminal", err)
	}
}
