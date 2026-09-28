# Optional desktop integration: registers shfm with the desktop as a file
# manager like any other, next to those already installed. Which one is
# used stays the system's choice. See "Desktop integration" in the
# top-level README.md.
#
#   sudo make install
#       shfm.desktop, declaring shfm able to open folders (inode/directory):
#       used when it's the default application for folders, or the only
#       one; and the xdg-desktop-portal file chooser backend: used when the
#       portal's configuration picks it, or it's the only one.
#
#   sudo make install-filemanager1
#       the org.freedesktop.FileManager1 D-Bus service ("Show in folder").
#       Separate because the bus has no notion of a default among several
#       providers: with another file manager's installed too (Thunar,
#       Dolphin, Nautilus...), which one it starts is unspecified, and may
#       well be shfm.
#
#   sudo make uninstall     removes both

SHFM ?= $(shell command -v shfm 2>/dev/null || echo /usr/bin/shfm)
PREFIX ?= /usr
DATADIR ?= $(PREFIX)/share

SERVICES = shfm.FileManager1.service org.freedesktop.impl.portal.desktop.shfm.service

all: $(SERVICES)

%.service: %.service.in
	sed 's|@SHFM@|$(SHFM)|' $< > $@

install: org.freedesktop.impl.portal.desktop.shfm.service
	install -Dm644 ../../shfm.desktop $(DESTDIR)$(DATADIR)/applications/shfm.desktop
	install -Dm644 org.freedesktop.impl.portal.desktop.shfm.service $(DESTDIR)$(DATADIR)/dbus-1/services/org.freedesktop.impl.portal.desktop.shfm.service
	install -Dm644 shfm.portal $(DESTDIR)$(DATADIR)/xdg-desktop-portal/portals/shfm.portal
	if [ -z "$(DESTDIR)" ] && command -v update-desktop-database >/dev/null; then update-desktop-database -q $(DATADIR)/applications; fi

install-filemanager1: shfm.FileManager1.service
	install -Dm644 shfm.FileManager1.service $(DESTDIR)$(DATADIR)/dbus-1/services/shfm.FileManager1.service

uninstall:
	rm -f $(DESTDIR)$(DATADIR)/applications/shfm.desktop
	rm -f $(DESTDIR)$(DATADIR)/dbus-1/services/shfm.FileManager1.service
	rm -f $(DESTDIR)$(DATADIR)/dbus-1/services/org.freedesktop.impl.portal.desktop.shfm.service
	rm -f $(DESTDIR)$(DATADIR)/xdg-desktop-portal/portals/shfm.portal
	if [ -z "$(DESTDIR)" ] && command -v update-desktop-database >/dev/null; then update-desktop-database -q $(DATADIR)/applications; fi

clean:
	rm -f $(SERVICES)

.PHONY: all install install-filemanager1 uninstall clean
