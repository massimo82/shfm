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
	"path"
	"strings"

	"shfm/internal/nerdfont"
	"shfm/internal/vfs"
)

// findNerdFont is a variable so tests can fake the installed fonts.
var findNerdFont = nerdfont.Find

// Nerd Font icons (https://www.nerdfonts.com/), shown in place of the
// bracketed pseudo-icons when the config's nerd_icons is on. They're
// monochrome: drawn in the color of the entry's name. Each comment is the
// glyph's name in the Nerd Fonts cheat sheet.
const (
	nerdFolder      = ""          // fa-folder
	nerdParent      = ""          // fa-level_up
	nerdFile        = ""          // fa-file_o
	nerdLinkFile    = ""          // oct-file_symlink_file
	nerdLinkDir     = ""          // oct-file_symlink_directory
	nerdTrash       = ""          // fa-trash
	nerdExecutable  = ""          // oct-file_binary
	nerdTextFile    = ""          // fa-file_text_o
	nerdImageFile   = ""          // fa-file_picture_o
	nerdAudioFile   = ""          // fa-file_audio_o
	nerdVideoFile   = ""          // fa-file_video_o
	nerdArchiveFile = ""          // fa-file_zipper
	nerdShell       = ""          // oct-terminal
	nerdGit         = ""          // fa-git
	nerdDocker      = ""          // linux-docker
	nerdDesktopDir  = ""          // fa-desktop
	nerdDocumentDir = "\U000f0c82" // md-folder_text
	nerdDownloadDir = "\U000f024d" // md-folder_download
	nerdMusicDir    = "\U000f1359" // md-folder_music
	nerdPictureDir  = "\U000f024f" // md-folder_image
	nerdVideoDir    = "\U000f19fa" // md-folder_play
	nerdTemplateDir = "\U000f19f6" // md-folder_file
	nerdPublicDir   = "\U000f024c" // md-folder_account
)

// nerdXDGDirIcons gives each standard XDG user directory, by its
// user-dirs.dirs key, an icon of its own.
var nerdXDGDirIcons = map[string]string{
	"XDG_DESKTOP_DIR":     nerdDesktopDir,
	"XDG_DOCUMENTS_DIR":   nerdDocumentDir,
	"XDG_DOWNLOAD_DIR":    nerdDownloadDir,
	"XDG_MUSIC_DIR":       nerdMusicDir,
	"XDG_PICTURES_DIR":    nerdPictureDir,
	"XDG_VIDEOS_DIR":      nerdVideoDir,
	"XDG_TEMPLATES_DIR":   nerdTemplateDir,
	"XDG_PUBLICSHARE_DIR": nerdPublicDir,
}

// nerdNameIcons matches whole file names, lowercase, before extensions:
// the files known by name rather than by type.
var nerdNameIcons = map[string]string{
	"makefile":           "", // seti-makefile
	"gnumakefile":        "",
	"cmakelists.txt":     "", // dev-cmake
	"dockerfile":         nerdDocker,
	"containerfile":      nerdDocker,
	"docker-compose.yml": nerdDocker,
	"compose.yaml":       nerdDocker,
	".dockerignore":      nerdDocker,
	".gitignore":         nerdGit,
	".gitattributes":     nerdGit,
	".gitmodules":        nerdGit,
	".gitconfig":         nerdGit,
	"license":            "", // seti-license
	"licence":            "",
	"copying":            "",
	"go.mod":             "", // seti-go
	"go.sum":             "",
	"cargo.toml":         "", // dev-rust
	"cargo.lock":         "",
	"pkgbuild":           "", // linux-archlinux
	".bashrc":            nerdShell,
	".bash_profile":      nerdShell,
	".zshrc":             nerdShell,
	".profile":           nerdShell,
	".editorconfig":      "", // seti-editorconfig
}

// nerdExtIcons matches lowercase extensions, dot included.
var nerdExtIcons = map[string]string{
	// Text and documents.
	".txt":      nerdTextFile,
	".log":      nerdTextFile,
	".nfo":      nerdTextFile,
	".md":       "", // oct-markdown
	".markdown": "",
	".org":      "", // custom-orgmode
	".tex":      "", // seti-tex
	".pdf":      "", // fa-file_pdf_o
	".doc":      "", // fa-file_word_o
	".docx":     "",
	".odt":      "",
	".rtf":      "",
	".xls":      "", // fa-file_excel_o
	".xlsx":     "",
	".ods":      "",
	".csv":      "", // seti-csv
	".tsv":      "",
	".ppt":      "", // fa-file_powerpoint_o
	".pptx":     "",
	".odp":      "",
	".epub":     "", // fa-book
	".mobi":     "",
	".djvu":     "",

	// Images, audio, video.
	".png":  nerdImageFile,
	".jpg":  nerdImageFile,
	".jpeg": nerdImageFile,
	".gif":  nerdImageFile,
	".bmp":  nerdImageFile,
	".webp": nerdImageFile,
	".ico":  nerdImageFile,
	".tif":  nerdImageFile,
	".tiff": nerdImageFile,
	".heic": nerdImageFile,
	".avif": nerdImageFile,
	".xcf":  nerdImageFile,
	".psd":  nerdImageFile,
	".raw":  nerdImageFile,
	".cr2":  nerdImageFile,
	".nef":  nerdImageFile,
	".dng":  nerdImageFile,
	".svg":  "", // seti-svg
	".mp3":  nerdAudioFile,
	".wav":  nerdAudioFile,
	".flac": nerdAudioFile,
	".ogg":  nerdAudioFile,
	".oga":  nerdAudioFile,
	".opus": nerdAudioFile,
	".m4a":  nerdAudioFile,
	".aac":  nerdAudioFile,
	".wma":  nerdAudioFile,
	".mid":  nerdAudioFile,
	".midi": nerdAudioFile,
	".mp4":  nerdVideoFile,
	".m4v":  nerdVideoFile,
	".mkv":  nerdVideoFile,
	".avi":  nerdVideoFile,
	".mov":  nerdVideoFile,
	".webm": nerdVideoFile,
	".wmv":  nerdVideoFile,
	".flv":  nerdVideoFile,
	".mpg":  nerdVideoFile,
	".mpeg": nerdVideoFile,
	".ogv":  nerdVideoFile,

	// Archives, disk images, packages.
	".zip":      nerdArchiveFile,
	".tar":      nerdArchiveFile,
	".gz":       nerdArchiveFile,
	".tgz":      nerdArchiveFile,
	".bz2":      nerdArchiveFile,
	".xz":       nerdArchiveFile,
	".zst":      nerdArchiveFile,
	".lz":       nerdArchiveFile,
	".lz4":      nerdArchiveFile,
	".lzma":     nerdArchiveFile,
	".7z":       nerdArchiveFile,
	".rar":      nerdArchiveFile,
	".cab":      nerdArchiveFile,
	".cpio":     nerdArchiveFile,
	".jar":      "", // dev-java
	".iso":      "", // fa-hdd_o
	".img":      "",
	".deb":      "", // linux-debian
	".rpm":      "", // linux-redhat
	".apk":      "", // fa-android
	".appimage": "", // fa-linux
	".exe":      "", // fa-windows
	".msi":      "",
	".bat":      "",
	".dmg":      "", // fa-apple

	// Code.
	".sh":      nerdShell,
	".bash":    nerdShell,
	".zsh":     nerdShell,
	".fish":    nerdShell,
	".ksh":     nerdShell,
	".ps1":     "", // seti-powershell
	".go":      "", // seti-go
	".py":      "", // seti-python
	".pyw":     "",
	".pyi":     "",
	".ipynb":   "", // seti-notebook
	".rs":      "", // dev-rust
	".c":       "", // custom-c
	".h":       "",
	".cpp":     "", // custom-cpp
	".cc":      "",
	".cxx":     "",
	".hpp":     "",
	".hh":      "",
	".cs":      "", // dev-csharp
	".java":    "", // dev-java
	".kt":      "", // seti-kotlin
	".kts":     "",
	".scala":   "", // dev-scala
	".swift":   "", // dev-swift
	".js":      "", // dev-javascript_alt
	".mjs":     "",
	".cjs":     "",
	".ts":      "", // seti-typescript
	".jsx":     "", // seti-react
	".tsx":     "",
	".vue":     "", // seti-vue
	".svelte":  "", // seti-svelte
	".php":     "", // dev-php
	".rb":      "", // dev-ruby_rough
	".pl":      "", // dev-perl
	".pm":      "",
	".lua":     "", // seti-lua
	".hs":      "", // dev-haskell
	".dart":    "", // dev-dart
	".ex":      "", // seti-elixir
	".exs":     "",
	".erl":     "", // dev-erlang
	".elm":     "", // seti-elm
	".clj":     "", // dev-clojure
	".jl":      "", // seti-julia
	".r":       "", // seti-r
	".nim":     "", // seti-nim
	".zig":     "", // seti-zig
	".ml":      "", // seti-ocaml
	".asm":     "", // seti-asm
	".s":       "",
	".wasm":    "", // seti-wasm
	".vim":     "", // custom-vim
	".html":    "", // fa-html5
	".htm":     "",
	".css":     "", // fa-css3
	".scss":    "", // seti-sass
	".sass":    "",
	".less":    "", // seti-less
	".xml":     "", // fa-code
	".graphql": "", // seti-graphql
	".gql":     "",
	".tf":      "", // seti-terraform
	".diff":    "", // oct-file_diff
	".patch":   "",

	// Data and configuration.
	".json":   "", // cod-json
	".jsonc":  "",
	".json5":  "",
	".yaml":   "", // seti-yml
	".yml":    "",
	".toml":   "", // custom-toml
	".ini":    "", // seti-config
	".cfg":    "",
	".conf":   "",
	".sql":    "", // fa-database
	".db":     "",
	".sqlite": "",
	".lock":   "", // fa-lock
	".pem":    "", // fa-key
	".key":    "",
	".crt":    "",
	".pub":    "",
	".asc":    "",
	".gpg":    "",
	".ttf":    "", // fa-font
	".otf":    "",
	".woff":   "",
	".woff2":  "",
}

// specialTrash marks, in place of an XDG key, a folder holding a trash.
const specialTrash = "trash"

// isTrashDir tells whether the folder name, in dir, holds a trash: the
// home trash, ~/.local/share/Trash, or a volume's .Trash or .Trash-$UID
// (see the freedesktop.org Trash specification).
func isTrashDir(dir, name string) bool {
	if name == ".Trash" || strings.HasPrefix(name, ".Trash-") {
		return true
	}
	return name == "Trash" && strings.HasSuffix(path.Clean(dir), "/.local/share")
}

// nerdEntryIcon picks e's Nerd Font icon. special is the XDG key of a
// standard user directory, specialTrash for a trash folder, "" otherwise.
// Links come first, telling a link to a folder from one to a file; files
// are recognized by name, then by extension, then as executables, and
// get the generic file icon when nothing matches.
func nerdEntryIcon(e vfs.Entry, special string) string {
	switch {
	case IsParentEntry(e):
		return nerdParent
	case e.IsSymlink && e.IsDir:
		return nerdLinkDir
	case e.IsSymlink:
		return nerdLinkFile
	case e.IsDir && special == specialTrash:
		return nerdTrash
	case e.IsDir:
		if icon, ok := nerdXDGDirIcons[special]; ok {
			return icon
		}
		return nerdFolder
	}
	if icon, ok := nerdNameIcons[strings.ToLower(e.Name)]; ok {
		return icon
	}
	if icon, ok := nerdExtIcons[extOf(e.Name)]; ok {
		return icon
	}
	if e.Mode&0o111 != 0 {
		return nerdExecutable
	}
	return nerdFile
}
