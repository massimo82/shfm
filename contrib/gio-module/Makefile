# Optional GIO module listing shfm's network mounts in GTK file dialogs.
# See "Network sources in other applications" in the top-level README.md.

PKG_CONFIG ?= pkg-config
CFLAGS ?= -O2 -g
GIO_MODULE_DIR ?= $(shell $(PKG_CONFIG) --variable=giomoduledir gio-2.0)

MODULE = libshfm-volume-monitor.so

all: $(MODULE)

$(MODULE): shfm-volume-monitor.c
	$(CC) $(CPPFLAGS) $(CFLAGS) -Wall -Wextra -Wno-unused-parameter -fPIC -shared \
		$(shell $(PKG_CONFIG) --cflags gio-2.0 gio-unix-2.0) \
		-o $@ $< $(LDFLAGS) $(shell $(PKG_CONFIG) --libs gio-2.0 gio-unix-2.0)

# GIO only loads modules listed in the folder's giomodule.cache: refresh it
# (a package manager does this itself, e.g. pacman's gio-querymodules hook;
# with DESTDIR set, leave it to it).
install: $(MODULE)
	@test -n "$(GIO_MODULE_DIR)" || { echo "GIO's module folder is unknown: install GLib's development files (pkg-config gio-2.0), or set GIO_MODULE_DIR (gio-querymodules --help shows it, e.g. /usr/lib/x86_64-linux-gnu/gio/modules)" >&2; exit 1; }
	install -Dm755 $(MODULE) $(DESTDIR)$(GIO_MODULE_DIR)/$(MODULE)
	if [ -z "$(DESTDIR)" ]; then gio-querymodules $(GIO_MODULE_DIR); fi

uninstall:
	@test -n "$(GIO_MODULE_DIR)" || { echo "GIO's module folder is unknown: set GIO_MODULE_DIR" >&2; exit 1; }
	rm -f $(DESTDIR)$(GIO_MODULE_DIR)/$(MODULE)
	if [ -z "$(DESTDIR)" ]; then gio-querymodules $(GIO_MODULE_DIR); fi

clean:
	rm -f $(MODULE)

.PHONY: all install uninstall clean
