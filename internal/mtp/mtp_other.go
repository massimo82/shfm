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

//go:build !linux

package mtp

import "fmt"

// DeviceInfo describes an MTP device found on the USB bus (Linux only for
// now: detection relies on information exposed by sysfs).
type DeviceInfo struct {
	Bus, Addr    int
	DevNode      string
	Interface    int
	EPIn, EPOut  byte
	MaxPacketIn  int
	MaxPacketOut int
	Manufacturer string
	Product      string
	VendorID     string
	ProductID    string
}

// Label returns a human-readable description of the device.
func (d DeviceInfo) Label() string { return d.Manufacturer + " " + d.Product }

// Device represents an open MTP session (Linux only for now).
type Device struct{}

// DiscoverDevices is not supported on this platform: detection is
// implemented via sysfs (/sys/bus/usb/devices) and usbfs ioctls,
// available only on Linux.
func DiscoverDevices() ([]DeviceInfo, error) { return nil, nil }

// Open is not supported on this platform.
func Open(info DeviceInfo) (*Device, error) {
	return nil, fmt.Errorf("MTP support is not available on this platform")
}

// Close is not supported on this platform.
func (d *Device) Close() error {
	return fmt.Errorf("MTP support is not available on this platform")
}
