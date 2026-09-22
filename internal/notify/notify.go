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

// Package notify posts desktop notifications via the freedesktop.org
// Notifications D-Bus interface (org.freedesktop.Notifications on the
// session bus). Every DE/WM notification daemon implements this same
// interface regardless of display server — mako, dunst, swaync on
// Wayland compositors, and GNOME Shell/Plasma/xfce4-notifyd elsewhere —
// so, unlike copy/paste, nothing Wayland-specific is needed here: talking
// to the session bus directly (via github.com/godbus/dbus/v5, already used
// the same way for udisks2 in internal/drives) avoids depending on an
// external notify-send binary being installed.
package notify

import "github.com/godbus/dbus/v5"

const (
	notifyDest  = "org.freedesktop.Notifications"
	notifyPath  = "/org/freedesktop/Notifications"
	notifyIface = notifyDest
)

// Urgency mirrors the notification spec's urgency levels (used as the
// "urgency" hint, a byte: 0=low, 1=normal, 2=critical). Most daemons use
// this to decide whether the notification persists until dismissed.
type Urgency byte

const (
	Low      Urgency = 0
	Normal   Urgency = 1
	Critical Urgency = 2
)

// Send posts a single desktop notification with the given summary (title),
// body text and urgency. It opens a short-lived session bus connection per
// call — notifications are infrequent (task completions), so there's no
// need to keep one open for the process lifetime.
//
// Errors are returned rather than logged so callers can decide how to
// react; the expected failure mode is simply "no session bus" (e.g. a
// headless/SSH shfm with no notification daemon running), which callers
// should treat as a silent no-op rather than surfacing to the user — a
// failed *notification about* a failure shouldn't become a second failure.
func Send(summary, body string, urgency Urgency) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()

	obj := conn.Object(notifyDest, dbus.ObjectPath(notifyPath))
	hints := map[string]dbus.Variant{
		"urgency": dbus.MakeVariant(byte(urgency)),
	}
	call := obj.Call(notifyIface+".Notify", 0,
		"shfm",                // app_name
		uint32(0),             // replaces_id
		"system-file-manager", // app_icon (standard freedesktop icon name)
		summary,
		body,
		[]string{}, // actions
		hints,
		int32(-1), // expire_timeout: daemon default
	)
	return call.Err
}
