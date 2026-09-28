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

package portal

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"

	"shfm/internal/pick"
)

// wire round-trips options through D-Bus's encoding, so they arrive as
// the bus delivers them (structs as []any...), not as the Go values built
// here.
func wire(t *testing.T, options results) results {
	t.Helper()
	msg := &dbus.Message{Type: dbus.TypeMethodCall, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:      dbus.MakeVariant(dbus.ObjectPath("/x")),
		dbus.FieldMember:    dbus.MakeVariant("M"),
		dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(options)),
	}, Body: []any{options}}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
	decoded, err := dbus.DecodeMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Body[0].(map[string]dbus.Variant)
}

func TestParseOpenOptions(t *testing.T) {
	pdf := filterTuple{"PDF", []patternTuple{{0, "*.pdf"}}}
	images := filterTuple{"Images", []patternTuple{{1, "image/*"}}}
	opts := wire(t, results{
		"accept_label":   dbus.MakeVariant("_Upload"),
		"multiple":       dbus.MakeVariant(true),
		"current_folder": dbus.MakeVariant([]byte("/home/u/Downloads\x00")),
		"filters":        dbus.MakeVariant([]filterTuple{pdf, images}),
		"current_filter": dbus.MakeVariant(images),
		"choices": dbus.MakeVariant([]choiceTuple{
			{"enc", "Encoding", []choiceOptionTuple{{"utf8", "UTF-8"}, {"latin1", "Latin-1"}}, "latin1"},
			{"ro", "Read only", nil, "true"},
		}),
	})
	got := parseOptions(pick.ModeOpen, "Upload", opts)
	want := pick.Request{
		Mode: pick.ModeOpen, Title: "Upload", AcceptLabel: "_Upload", Multiple: true,
		CurrentFolder: "/home/u/Downloads",
		Filters: []pick.Filter{
			{Name: "PDF", Patterns: []pick.Pattern{{Kind: 0, Pattern: "*.pdf"}}},
			{Name: "Images", Patterns: []pick.Pattern{{Kind: 1, Pattern: "image/*"}}},
		},
		CurrentFilter: 1,
		Choices: []pick.Choice{
			{ID: "enc", Label: "Encoding", Options: []pick.ChoiceOption{{ID: "utf8", Label: "UTF-8"}, {ID: "latin1", Label: "Latin-1"}}, Default: "latin1"},
			{ID: "ro", Label: "Read only", Default: "true"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseOptions =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseCurrentFilterAlone(t *testing.T) {
	only := filterTuple{"Text", []patternTuple{{0, "*.txt"}}}
	got := parseOptions(pick.ModeOpen, "", wire(t, results{"current_filter": dbus.MakeVariant(only)}))
	if len(got.Filters) != 1 || got.CurrentFilter != 0 || got.Filters[0].Name != "Text" {
		t.Fatalf("a lone current_filter should apply: %+v", got)
	}
}

func TestParseSaveOptions(t *testing.T) {
	got := parseOptions(pick.ModeSave, "Save", wire(t, results{
		"current_name": dbus.MakeVariant("report.pdf"),
		"current_file": dbus.MakeVariant([]byte("/tmp/old.pdf\x00")),
		"directory":    dbus.MakeVariant(true), // not a SaveFile option
	}))
	if got.CurrentName != "report.pdf" || got.CurrentFile != "/tmp/old.pdf" || got.Directory || got.CurrentFilter != -1 {
		t.Fatalf("parseOptions = %+v", got)
	}
}

func TestParseSaveFilesOptions(t *testing.T) {
	got := parseOptions(pick.ModeSaveFiles, "", wire(t, results{
		"files": dbus.MakeVariant([][]byte{[]byte("a.txt\x00"), []byte("b.txt\x00")}),
	}))
	if !reflect.DeepEqual(got.Files, []string{"a.txt", "b.txt"}) {
		t.Fatalf("Files = %q", got.Files)
	}
}

func TestReplyResults(t *testing.T) {
	req := pick.Request{
		Filters: []pick.Filter{{Name: "PDF", Patterns: []pick.Pattern{{Kind: 0, Pattern: "*.pdf"}}}},
		Choices: []pick.Choice{{ID: "ro", Label: "Read only"}},
	}
	got := replyResults(req, pick.Reply{
		Paths:   []string{"/home/u/My Docs/a#1.pdf"},
		Filter:  0,
		Choices: map[string]string{"ro": "true"},
	})
	if uris := got["uris"].Value().([]string); !reflect.DeepEqual(uris, []string{"file:///home/u/My%20Docs/a%231.pdf"}) {
		t.Fatalf("uris = %q", uris)
	}
	if sig := got["current_filter"].Signature().String(); sig != "(sa(us))" {
		t.Fatalf("current_filter signature = %s", sig)
	}
	if sig := got["choices"].Signature().String(); sig != "a(ss)" {
		t.Fatalf("choices signature = %s", sig)
	}
}
