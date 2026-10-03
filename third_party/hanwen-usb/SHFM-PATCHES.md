# shfm patches to github.com/hanwen/usb v0.0.0-20141217151552-69aee4530ac7

Vendored copy used through github.com/hanwen/go-mtpfs by shfm's
`internal/mtp`, wired in with a `replace` in shfm's go.mod.
Upstream license: BSD-3-Clause (see LICENSE).

- Removed `usb_test.go`.
- `usb.go` (`GetDeviceList`, `DeviceList.Done`): with no USB device at
  all, `Done` indexed the empty list (`d[0]`) and panicked — crashing shfm
  whenever MTP devices were looked for on a machine without USB devices
  (a VM, a container, a CI runner). The list's capacity now covers
  libusb's NULL terminator, and `Done` takes its address through it.

When upgrading, re-apply these changes on top of the new upstream version.
