# shfm patches to github.com/gokrazy/rsync v0.3.8

Vendored copy used by shfm's `internal/mirror` (in-process local mirror via
`rsyncclient` + `rsyncd`), wired in with a `replace` in shfm's go.mod.
Upstream license: BSD-3-Clause (see LICENSE).

- Removed `cmd/`, `integration/`, `testdata/`, `systemd/`, all tests and
  test-only helper packages.
- Removed `internal/maincmd/listeners_linux.go`: no systemd socket
  activation, no dependency on github.com/coreos/go-systemd (the generic
  `listeners.go` stub is used on every OS).
- `internal/restrict` is a no-op: no Landlock, no dependency on
  github.com/landlock-lsm/go-landlock and libcap/psx.
- `internal/rsyncopts/serveroptions.go`: forward `--delete` to the server.
- `internal/maincmd/clientmaincmd.go` (`ClientRun`): a client acting as
  sender sends the filter list when `--delete` is set, as the receiver
  expects (upstream deadlocked).
- `internal/receiver/do.go` (`deleteFiles`): return `fs.SkipDir` only for
  deleted directories; upstream also returned it after deleting a file,
  which skipped the rest of that folder and left stale entries behind.

When upgrading, re-apply these changes on top of the new upstream version.
