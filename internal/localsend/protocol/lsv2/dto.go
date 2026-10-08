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

// Package lsv2 is version 2 of the LocalSend protocol, as of 2.2
// (https://github.com/localsend/protocol): a client and a server
// implementing internal/localsend/protocol's interfaces, and the
// multicast announcements' format.
package lsv2

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"shfm/internal/localsend/protocol"
)

// Version is the protocol version announced.
const Version = "2.2"

// Speaks reports whether a device announcing version talks to this
// package: any 2.x.
func Speaks(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	return major == "2"
}

// API paths.
const (
	pathRegister      = "/api/localsend/v2/register"
	pathInfo          = "/api/localsend/v2/info"
	pathPrepareUpload = "/api/localsend/v2/prepare-upload"
	pathUpload        = "/api/localsend/v2/upload"
	pathCancel        = "/api/localsend/v2/cancel"
)

// registerDTO is a device's info, as announced, sent to /register and
// in prepare-upload requests.
type registerDTO struct {
	Alias       string `json:"alias"`
	Version     string `json:"version"`
	DeviceModel string `json:"deviceModel,omitempty"`
	DeviceType  string `json:"deviceType,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Download    bool   `json:"download"`
	// Announce is only in multicast messages.
	Announce *bool `json:"announce,omitempty"`
}

// infoDTO answers /register and /info: no port or protocol, which the
// asker knows already.
type infoDTO struct {
	Alias       string `json:"alias"`
	Version     string `json:"version"`
	DeviceModel string `json:"deviceModel,omitempty"`
	DeviceType  string `json:"deviceType,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Download    bool   `json:"download"`
}

type metadataDTO struct {
	Modified string `json:"modified,omitempty"`
	Accessed string `json:"accessed,omitempty"`
}

type fileDTO struct {
	ID       string       `json:"id"`
	FileName string       `json:"fileName"`
	Size     int64        `json:"size"`
	FileType string       `json:"fileType"`
	SHA256   string       `json:"sha256,omitempty"`
	Preview  *string      `json:"preview,omitempty"`
	Metadata *metadataDTO `json:"metadata,omitempty"`
}

type prepareUploadRequest struct {
	Info  registerDTO        `json:"info"`
	Files map[string]fileDTO `json:"files"`
}

type prepareUploadResponse struct {
	SessionID string            `json:"sessionId"`
	Files     map[string]string `json:"files"`
}

func toRegister(i protocol.Info) registerDTO {
	proto := "http"
	if i.HTTPS {
		proto = "https"
	}
	return registerDTO{
		Alias: i.Alias, Version: Version, DeviceModel: i.DeviceModel, DeviceType: i.DeviceType,
		Fingerprint: i.Fingerprint, Port: i.Port, Protocol: proto, Download: i.Download,
	}
}

func (d registerDTO) info() protocol.Info {
	return protocol.Info{
		Alias: d.Alias, Version: d.Version, DeviceModel: d.DeviceModel, DeviceType: normalizeType(d.DeviceType),
		Fingerprint: d.Fingerprint, Port: d.Port, HTTPS: d.Protocol != "http", Download: d.Download,
	}
}

func toInfoDTO(i protocol.Info) infoDTO {
	return infoDTO{Alias: i.Alias, Version: Version, DeviceModel: i.DeviceModel, DeviceType: i.DeviceType,
		Fingerprint: i.Fingerprint, Download: i.Download}
}

// normalizeType maps an unknown device type to a desktop, as the
// protocol asks.
func normalizeType(t string) string {
	switch t {
	case "", protocol.TypeMobile, protocol.TypeDesktop, protocol.TypeWeb, protocol.TypeHeadless, protocol.TypeServer:
		return t
	}
	return protocol.TypeDesktop
}

func toFileDTO(f protocol.File) fileDTO {
	d := fileDTO{ID: f.ID, FileName: f.Name, Size: f.Size, FileType: f.MIME, SHA256: f.SHA256, Preview: f.Preview}
	if !f.Modified.IsZero() || !f.Accessed.IsZero() {
		d.Metadata = &metadataDTO{Modified: formatTime(f.Modified), Accessed: formatTime(f.Accessed)}
	}
	return d
}

func (d fileDTO) file() protocol.File {
	f := protocol.File{ID: d.ID, Name: d.FileName, Size: d.Size, MIME: d.FileType,
		SHA256: strings.ToLower(d.SHA256), Preview: d.Preview}
	if d.Metadata != nil {
		f.Modified = parseTime(d.Metadata.Modified)
		f.Accessed = parseTime(d.Metadata.Accessed)
	}
	return f
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// EncodeAnnouncement is the multicast message for a.
func EncodeAnnouncement(a protocol.Announcement) []byte {
	d := toRegister(a.Info)
	d.Announce = &a.Announce
	data, _ := json.Marshal(d)
	return data
}

// DecodeAnnouncement parses a multicast message.
func DecodeAnnouncement(data []byte) (protocol.Announcement, error) {
	var d registerDTO
	if err := json.Unmarshal(data, &d); err != nil {
		return protocol.Announcement{}, err
	}
	if d.Fingerprint == "" || d.Port <= 0 || d.Port > 65535 {
		return protocol.Announcement{}, errors.New("incomplete announcement")
	}
	// Version 1 had no "announce", only "announcement"; a 2.x device
	// always sends it.
	a := protocol.Announcement{Info: d.info(), Announce: d.Announce != nil && *d.Announce}
	return a, nil
}
