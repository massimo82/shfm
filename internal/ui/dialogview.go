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

package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"shfm/internal/config"
	"shfm/internal/drives"
)

// helpEntry is one "key — action" row in the compact help dialog.
type helpEntry struct{ key, desc string }

// mouseHints adds a mouse equivalent to a handful of keyboard actions' help
// row (e.g. "Ctrl+P / click PATH") — purely a display aid, not something
// keybindings.conf governs, since these clicks aren't remappable.
var mouseHints = map[config.Action]string{
	config.ActionActivate:        "dbl-click",
	config.ActionEditPath:        "click PATH",
	config.ActionSourceMenu:      "click SOURCE",
	config.ActionToggleLayout:    "click title",
	config.ActionToggleTrashView: "click [T]",
}

// mouseOnlyHelpEntries are rows with no keyboard equivalent at all, so
// they're not part of the configurable catalogue (internal/config's
// KeyMap) and are always shown as-is.
var mouseOnlyHelpEntries = []helpEntry{
	{"[+] by PATH", "new file/folder picker"},
	{"Click+Ctrl", "select one"},
	{"Click+Shift", "select range"},
	{"Drag & drop", "move (Ctrl: copy)"},
	{"Mouse wheel", "scroll list"},
}

// buildHelpEntries turns the active keymap into the Help dialog's rows —
// one per configurable action, in the same order as keybindings.conf lists
// them, always showing whatever keys are actually bound (so Help can never
// drift out of sync with a customized keybindings.conf) — plus the fixed
// mouse-only rows above. An action the user disabled (bound to no keys) is
// simply omitted: there's nothing to show for it.
func (m *Model) buildHelpEntries() []helpEntry {
	entries := make([]helpEntry, 0, len(mouseOnlyHelpEntries)+32)
	for _, kb := range m.keymap.All() {
		if len(kb.Keys) == 0 {
			continue
		}
		labels := make([]string, len(kb.Keys))
		for i, k := range kb.Keys {
			labels[i] = prettyKey(k)
		}
		key := strings.Join(labels, " / ")
		if hint, ok := mouseHints[kb.Action]; ok {
			key += " / " + hint
		}
		entries = append(entries, helpEntry{key: key, desc: strings.ToLower(kb.Description)})
	}
	entries = append(entries, mouseOnlyHelpEntries...)
	return entries
}

// prettyKey renders a key string (as tea.KeyMsg.String() and
// keybindings.conf spell it, e.g. "ctrl+up", "ctrl+alt+h", "space") the
// way the rest of shfm's UI capitalizes shortcuts, e.g. "Ctrl+Up",
// "Ctrl+Alt+H", "Space".
func prettyKey(k string) string {
	parts := strings.Split(config.NormalizeKey(k), "+")
	for i, p := range parts {
		parts[i] = prettyKeySegment(p)
	}
	return strings.Join(parts, "+")
}

func prettyKeySegment(s string) string {
	switch s {
	case "ctrl", "alt", "shift":
		return strings.ToUpper(s[:1]) + s[1:]
	case "up", "down", "left", "right", "enter", "tab", "home", "end", "space":
		return strings.ToUpper(s[:1]) + s[1:]
	case "esc":
		return "Esc"
	case "backspace":
		return "Backspace"
	case "pgup":
		return "PgUp"
	case "pgdown":
		return "PgDown"
	default:
		if len(s) == 1 {
			return s // preserve case: single letters are case-sensitive shortcuts (a vs A)
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
}

// renderHelpColumns lays entries out as two columns of "key  desc" rows,
// sized to fit within maxWidth — a compact grid instead of one long,
// wrapping paragraph. Every row is padded/truncated to an exact cell width
// (with a safety margin) so lipgloss never has to word-wrap a row itself,
// which would otherwise stagger the two columns out of alignment.
// helpKeyWidth is the help's key column width: the longest key label,
// within limits.
func helpKeyWidth(entries []helpEntry) int {
	w := 8
	for _, e := range entries {
		w = maxInt(w, lipgloss.Width(e.key))
	}
	return min(w, 26)
}

// helpFullWidth is the Help dialog width (see dialogBox) at which no key
// or description gets truncated — the inverse of renderHelpColumns'
// column math, plus the box's padding.
func helpFullWidth(entries []helpEntry) int {
	desc := 0
	for _, e := range entries {
		desc = maxInt(desc, lipgloss.Width(e.desc))
	}
	col := helpKeyWidth(entries) + 1 + desc
	return 2*(col+2) + 4
}

func renderHelpColumns(entries []helpEntry, maxWidth int) string {
	half := (len(entries) + 1) / 2
	left, right := entries[:half], entries[half:]

	// The key column is as wide as the longest key label (within limits),
	// leaving the rest of each column to the descriptions.
	keyWidth := helpKeyWidth(entries)
	colWidth := maxWidth/2 - 2 // safety margin against rounding/join spacing
	descWidth := maxInt(colWidth-keyWidth-1, 4)

	renderCol := func(entries []helpEntry) string {
		var b strings.Builder
		for i, e := range entries {
			if i > 0 {
				b.WriteString("\n")
			}
			key := styleHelpKey.Render(padRight(truncate(e.key, keyWidth), keyWidth))
			desc := styleHelpDesc.Render(padRight(truncate(e.desc, descWidth), descWidth))
			b.WriteString(key + " " + desc)
		}
		return b.String()
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, renderCol(left), "  ", renderCol(right))
}

func (m *Model) renderDialogBox() string {
	d := m.dialog
	var b strings.Builder
	if d.Title != "" {
		b.WriteString(styleAccent.Render(d.Title))
		b.WriteString("\n\n")
	}
	switch d.Kind {
	case DialogRename, DialogNewFile, DialogNewFolder:
		b.WriteString(d.Inputs[0].View())
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Enter confirm · Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogSearch:
		b.WriteString(d.Inputs[0].View())
		b.WriteString("\n\n")
		matchMode := "Contains"
		if d.SearchRegex {
			matchMode = "Regex"
		}
		scope := "Current folder"
		if d.SearchRecursive {
			scope = "Recursive"
		}
		b.WriteString(styleDim.Render(fmt.Sprintf("Match: %s (Tab)  ·  Scope: %s (Ctrl+R)", matchMode, scope)))
		b.WriteString("\n")
		if d.SearchRecursive {
			b.WriteString(styleDim.Render("Enter search · Esc cancel"))
		} else {
			b.WriteString(styleDim.Render("live filter — Enter/Esc close"))
		}
		return dialogBox(64).Render(b.String())

	case DialogSemanticSearch:
		b.WriteString(d.Inputs[0].View())
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Semantic search: TXT/MD/TEX/PDF/DOCX/archives (+DOC/RTF/ODT with pandoc/LibreOffice) · Enter search · Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogNewChoice:
		for i, it := range d.Items {
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(it) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("\u2191/\u2193 or click move · Enter/click select · Esc cancel"))
		return dialogBox(40).Render(b.String())

	case DialogConfirmTrash, DialogConfirmPermanent, DialogConfirmEmptyTrash:
		b.WriteString(d.Message)
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("y / Enter confirm · n / Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogConfirmQuit:
		b.WriteString(styleWarn.Render(d.Message))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("y / Enter quit anyway · n / Esc stay"))
		return dialogBox(60).Render(b.String())

	case DialogConnectSMB, DialogConnectNFS, DialogConnectSFTP:
		var labels []string
		switch d.Kind {
		case DialogConnectSMB:
			labels = []string{"Host", "Share", "Domain", "User", "Password"}
		case DialogConnectNFS:
			labels = []string{"Host", "Export path", "UID", "GID"}
		case DialogConnectSFTP:
			labels = []string{"Host", "Port", "User", "Password", "Remote path"}
		}
		for i, inp := range d.Inputs {
			marker := "  "
			if i == d.FocusIdx {
				marker = "\u25b8 "
			}
			b.WriteString(fmt.Sprintf("%s%-14s %s\n", marker, labels[i]+":", inp.View()))
		}
		if d.Kind == DialogConnectSMB {
			b.WriteString(styleDim.Render("  (if entered, the password is saved encrypted)"))
			b.WriteString("\n")
		}
		if d.Kind == DialogConnectSFTP {
			b.WriteString(styleDim.Render("  (host key verified/recorded via ~/.ssh/known_hosts)"))
			b.WriteString("\n")
		}
		if d.IsError && d.Message != "" {
			b.WriteString("\n" + styleErr.Render(truncate(d.Message, 56)) + "\n")
		}
		b.WriteString(styleDim.Render("Tab or click field · Enter connect · Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogSourceMenu:
		for i, it := range d.Items {
			if i > 0 && sourceMenuGroup(m.sourceMenuEntries[i].kind) != sourceMenuGroup(m.sourceMenuEntries[i-1].kind) {
				b.WriteString("\n")
			}
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(it) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("\u2191/\u2193 or click move · Enter/click select · Esc cancel"))
		return dialogBox(72).Render(b.String())

	case DialogChooseApp:
		b.WriteString(styleDim.Render("No default application is set for " + d.ChooseAppMime + "."))
		b.WriteString("\n\n")
		for i, it := range d.Items {
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(it) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("The choice is remembered for next time. Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogConnecting:
		b.WriteString(d.Message)
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("This can take a moment (USB/network I/O) —\nthe rest of shfm stays fully usable meanwhile.\nEsc: keep working, apply the connection when it's ready."))
		return dialogBox(56).Render(b.String())

	case DialogProgress:
		t := m.taskByID(d.TaskID)
		if t == nil {
			b.WriteString("(task no longer available)")
			return dialogBox(60).Render(b.String())
		}
		if t.Label != "" {
			b.WriteString(truncate(t.Label, 56) + "\n")
		} else {
			b.WriteString(fmt.Sprintf("%s\n", t.Kind))
		}
		b.WriteString(renderProgressBar(48, t.Done, t.Total))
		b.WriteString(fmt.Sprintf("  %d/%d\n", t.Done, t.Total))
		b.WriteString(styleDim.Render(truncate(t.CurrentName, 56)) + "\n")
		if t.ErrorCount > 0 {
			b.WriteString(styleErr.Render(fmt.Sprintf("%d error(s) so far", t.ErrorCount)) + "\n")
		}
		if t.Finished {
			status := "Done."
			if t.Cancelled {
				status = "Cancelled."
			}
			b.WriteString("\n" + styleOK.Render(status) + " " + styleDim.Render("Press Enter or Esc to close."))
		} else {
			b.WriteString("\n" + styleDim.Render("Esc: send to background · c: cancel"))
		}
		return dialogBox(60).Render(b.String())

	case DialogTaskList:
		if len(d.Items) == 0 {
			b.WriteString(styleDim.Render("No background tasks."))
		}
		for i, it := range d.Items {
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(it) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("\u2191/\u2193 or click move · Enter/click open · Esc close"))
		return dialogBox(72).Render(b.String())

	case DialogProperties:
		b.WriteString(styleDim.Render(d.PropsPath) + "\n\n")
		e := d.PropsEntry
		kind := "File"
		if e.IsDir {
			kind = "Folder"
		} else if e.IsSymlink {
			kind = "Symlink"
		}
		b.WriteString(fmt.Sprintf("Type:      %s\n", kind))
		b.WriteString(fmt.Sprintf("Size:      %s\n", humanSize(e.Size)))
		b.WriteString(fmt.Sprintf("Modified:  %s\n", e.ModTime.Format("2006-01-02 15:04:05")))
		if len(d.PropsAttrs) > 0 {
			b.WriteString(fmt.Sprintf("Attributes: %s\n", styleErr.Render(strings.Join(d.PropsAttrs, ", "))))
			b.WriteString(styleDim.Render("(chattr flags: not even root can change or delete it;\n clear them first with chattr -i / -a)") + "\n")
		}
		if len(d.Inputs) >= 3 {
			b.WriteString("\n")
			propRow := func(i int, label string) {
				marker := "  "
				if i == d.FocusIdx {
					marker = "\u25b8 "
				}
				b.WriteString(fmt.Sprintf("%s%-11s %s\n", marker, label+":", d.Inputs[i].View()))
			}
			propRow(0, "Mode (octal)")
			propRow(1, "Owner")
			propRow(2, "Group")
			b.WriteString("\n" + styleDim.Render("Tab/click field · Enter apply · Esc close"))
		} else {
			b.WriteString(fmt.Sprintf("Mode:      %s\n", e.Mode.String()))
			b.WriteString(fmt.Sprintf("Owner:     %s\n", firstNonEmpty(e.Owner, "?")))
			b.WriteString(fmt.Sprintf("Group:     %s\n", firstNonEmpty(e.Group, "?")))
			b.WriteString("\n" + styleDim.Render("(editing permissions isn't supported on this source)\nEsc close"))
		}
		return dialogBox(56).Render(b.String())

	case DialogFormatChoose:
		b.WriteString(styleDim.Render(d.FormatDevice.Path) + "\n\n")
		for i, it := range d.Items {
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(it) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("\u2191/\u2193 or click move · Enter/click select · Esc cancel"))
		return dialogBox(48).Render(b.String())

	case DialogFormatConfirm1:
		b.WriteString(styleErr.Render(fmt.Sprintf(
			"WARNING: this will PERMANENTLY ERASE ALL DATA\non %s.\n\nA single %s partition will be created,\nreplacing everything currently on the device.",
			d.FormatDevice.Path, formatFSLabel(d.FormatFSType))))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("y / Enter continue · n / Esc cancel"))
		return dialogBox(56).Render(b.String())

	case DialogFormatConfirm2:
		b.WriteString(styleErr.Render(fmt.Sprintf(
			"LAST CHANCE: %s will be WIPED.\nThis cannot be undone.", d.FormatDevice.Path)))
		b.WriteString("\n\n")
		b.WriteString("Type " + styleErr.Render("YES") + " to proceed:\n")
		b.WriteString(d.Inputs[0].View())
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Enter confirm · Esc cancel"))
		return dialogBox(56).Render(b.String())

	case DialogMirrorConfirm:
		b.WriteString(d.Message)
		b.WriteString("\n\n")
		b.WriteString(styleWarn.Render("The destination will be kept IDENTICAL to the source:\nfiles missing from the source are DELETED from it.\nSynced now, then every 5 minutes while both are available."))
		b.WriteString("\n")
		if len(d.Items) > 0 {
			b.WriteString("\n")
			for i, it := range d.Items {
				prefix := "  "
				s := styleFile
				if i == d.ItemIdx {
					prefix, s = "\u25b8 ", styleAccent
				}
				b.WriteString(prefix + s.Render(it) + "\n")
			}
			b.WriteString("\n" + styleDim.Render("\u2191/\u2193 choose method · Enter create · Esc cancel"))
		} else {
			b.WriteString("\n" + styleDim.Render("Enter create · Esc cancel"))
		}
		return dialogBox(72).Render(b.String())

	case DialogMirrorList:
		if len(d.Items) == 0 {
			b.WriteString(styleDim.Render("No mirrors yet: copy (Ctrl+C) a file or folder,\nthen paste it as a mirror with this same shortcut."))
		}
		for i, it := range d.Items {
			prefix := "  "
			s := styleFile
			if i == d.ItemIdx {
				prefix, s = "\u25b8 ", styleAccent
			}
			b.WriteString(prefix + s.Render(truncate(it, 70)) + "\n")
		}
		b.WriteString("\n" + styleDim.Render("Enter sync now · p pause/resume · x delete · Esc close"))
		return dialogBox(80).Render(b.String())

	case DialogMirrorConfirmDelete:
		b.WriteString(d.Message)
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("y / Enter confirm · n / Esc cancel"))
		return dialogBox(64).Render(b.String())

	case DialogHelp:
		// As wide as needed to show every row in full, within the screen.
		entries := m.buildHelpEntries()
		w := min(m.width-8, helpFullWidth(entries))
		if w < 50 {
			w = 50
		}
		// dialogBox wraps content at width minus its own left+right
		// padding (2+2, see lipgloss's Style.Render) before the border goes
		// on — so laying out columns to the full w, same as the box's own
		// Width(w) below, leaves every row exactly 4 columns too wide and
		// wrapping onto a second line, staggering the two-column grid.
		b.WriteString(renderHelpColumns(entries, w-4))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("press any key to close"))
		return dialogBox(w).Render(b.String())

	case DialogMessage:
		b.WriteString(d.Message)
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("press any key to close"))
		return dialogBox(64).Render(b.String())
	}
	return dialogBox(64).Render(b.String())
}

// dialogBox is styleDialogBox for a box whose content area plus padding is
// w cells wide: lipgloss v2 widths include the border, v1's didn't.
func dialogBox(w int) lipgloss.Style {
	return styleDialogBox.Width(w + styleDialogBox.GetHorizontalBorderSize())
}

func formatFSLabel(t drives.FSType) string {
	for _, c := range drives.FormatChoices {
		if c.Type == t {
			return c.Label
		}
	}
	return string(t)
}

func firstNonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
