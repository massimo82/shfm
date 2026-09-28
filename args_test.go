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

package main

import (
	"reflect"
	"testing"

	"shfm/internal/ui"
)

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want options
	}{
		{nil, options{}},
		{[]string{"/mnt/data"}, options{start: ui.Start{Paths: []string{"/mnt/data"}}}},
		{[]string{"--select", "--", "/a", "-weird"}, options{start: ui.Start{Select: true, Paths: []string{"/a", "-weird"}}}},
		{[]string{"--properties", "file:///a"}, options{start: ui.Start{Properties: true, Paths: []string{"file:///a"}}}},
		{[]string{"--pick", "/run/s"}, options{pickSocket: "/run/s"}},
		{[]string{"--filemanager1"}, options{fileManager1: true}},
		{[]string{"--portal"}, options{portal: true}},
	} {
		got, err := parseArgs(tc.args)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
	for _, bad := range [][]string{{"--nope"}, {"--pick"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%q) should fail", bad)
		}
	}
}
