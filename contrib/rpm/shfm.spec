# shfm with every feature: semantic (content) search with Vulkan GPU
# acceleration (README, "Full build"), the desktop integration and the GIO
# module listing network sources in GTK file dialogs. For Fedora and
# derivatives. Build with network access (Go modules, and the Go toolchain
# go.mod asks for if the system's is older):
#
#   spectool -g -R contrib/rpm/shfm.spec
#   rpmbuild -ba contrib/rpm/shfm.spec
#
# llama.cpp is built for a generic CPU of the architecture (on x86_64 with
# AVX2/FMA/F16C, as llama.cpp's own generic builds). To build for this
# machine's CPU only: rpmbuild --with native ...

%bcond native 0

# llama-go pinned to the commit shfm's patches in third_party/llama-go target,
# and the llama.cpp commit that llama-go's submodule points at; the same as
# contrib/fetch-llama-go.sh and contrib/arch/shfm/PKGBUILD.in
%global llamago 992bbf8cfdc7484d4b3d724fa9133c5a87d6310d
%global llamago_sha256 6fd78f62efb2bd36bbd370f1be3fdda25b33cb7ee8076a9b5296fea4e513339c
%global llamacpp 90c26fcd4b2114b4aa39d09d69318cb8f438d27a
%global llamacpp_sha256 f679a75f99b6d2ca2efd25596c2d856745eec2a42c43cfdd7e00a373d40c4357

# Go's DWARF and the static llama.cpp libraries: no debuginfo package, and no
# LTO objects for Go's external link
%global debug_package %{nil}
%global _lto_cflags %{nil}

Name:           shfm
Version:        0.4.0
Release:        1%{?dist}
Summary:        Terminal file manager for local, MTP, network and cloud files, with encrypted vaults
License:        GPL-3.0-or-later AND MIT
URL:            https://github.com/massimo82/shfm
Source0:        %{url}/archive/refs/tags/v%{version}.tar.gz#/%{name}-%{version}.tar.gz
Source1:        https://github.com/tcpipuk/llama-go/archive/%{llamago}.tar.gz#/llama-go-%{llamago}.tar.gz
Source2:        https://github.com/ggml-org/llama.cpp/archive/%{llamacpp}.tar.gz#/llama.cpp-%{llamacpp}.tar.gz
ExclusiveArch:  x86_64 aarch64

BuildRequires:  golang
BuildRequires:  gcc
BuildRequires:  gcc-c++
BuildRequires:  make
BuildRequires:  cmake
BuildRequires:  pkgconf-pkg-config
BuildRequires:  libusb1-devel
BuildRequires:  glib2-devel
BuildRequires:  vulkan-headers
BuildRequires:  vulkan-loader-devel
BuildRequires:  glslc
BuildRequires:  spirv-headers-devel

Recommends:     mesa-vulkan-drivers
Recommends:     fuse3
Recommends:     udisks2
Recommends:     polkit
Recommends:     xdg-desktop-portal
Suggests:       shfm-models
Suggests:       xz
Suggests:       zstd
Suggests:       bzip2
Suggests:       lzip
Suggests:       lz4
Suggests:       bsdtar
Suggests:       7zip
Suggests:       pandoc
Suggests:       libreoffice

%description
Two-pane file manager for the terminal, talking directly to removable media,
MTP devices, SMB, NFS and SFTP shares and Google Drive, Dropbox and Microsoft
OneDrive accounts, with background tasks, archives, automatic mirrors, a
clipboard shared with the desktop, encrypted vaults in the age format (even
split across three sources) and local semantic search on file contents with
Vulkan GPU acceleration.

Registers with the desktop as a file manager next to the others (opening
folders, "Show in folder", the file chooser portal) and lists its network
sources in GTK file dialogs; which file manager is used stays the system's
choice.

%prep
echo "%{llamago_sha256}  %{SOURCE1}" | sha256sum -c
echo "%{llamacpp_sha256}  %{SOURCE2}" | sha256sum -c
%autosetup -n %{name}-%{version}
# Upstream llama-go merged into third_party/llama-go without overwriting the
# files carrying shfm's patches, then llama.cpp in place of its submodule
tar -xzf %{SOURCE1} -C ..
tar -xzf %{SOURCE2} -C ..
cp -an ../llama-go-%{llamago}/. third_party/llama-go/
rm -rf third_party/llama-go/llama.cpp
cp -a ../llama.cpp-%{llamacpp} third_party/llama-go/llama.cpp
cp third_party/llama-go/LICENSE llama-go.LICENSE
cp third_party/llama-go/llama.cpp/LICENSE llama.cpp.LICENSE

%build
%set_build_flags
export GOPATH=$PWD/.go/path GOCACHE=$PWD/.go/cache GOFLAGS=-modcacherw
# go.mod's Go version is fetched if the system's is older (Fedora's go.env
# turns that off, and the proxy it comes from)
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}
rm -f go.work go.work.sum
go work init . ./third_party/llama-go

(
  cd third_party/llama-go
%if %{with native}
  export CMAKE_ARGS='-DGGML_NATIVE=ON'
%else
  # UNAME_M keeps the Makefile's own -march/-mcpu=native off. The x86
  # extensions are named because ggml turns them all off when
  # SOURCE_DATE_EPOCH is set, as rpmbuild does
  export CMAKE_ARGS='-DGGML_NATIVE=OFF' UNAME_M=generic
%ifarch x86_64
  CMAKE_ARGS="$CMAKE_ARGS -DGGML_SSE42=ON -DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_BMI2=ON -DGGML_FMA=ON -DGGML_F16C=ON"
%endif
%endif
  CMAKE_BUILD_PARALLEL_LEVEL=%{_smp_build_ncpus} BUILD_TYPE=vulkan make libbinding.a
)

export CGO_CPPFLAGS="$CPPFLAGS" CGO_CFLAGS="$CFLAGS" CGO_CXXFLAGS="$CXXFLAGS" CGO_LDFLAGS="$LDFLAGS"
go build -buildmode=pie -trimpath -ldflags=-linkmode=external -tags 'semantic vulkan cloud vault' -o shfm .

make -C contrib/gio-module
make -C contrib/desktop-integration SHFM=%{_bindir}/shfm

%install
install -Dm755 shfm %{buildroot}%{_bindir}/shfm
make -C contrib/gio-module DESTDIR=%{buildroot} install
make -C contrib/desktop-integration DESTDIR=%{buildroot} SHFM=%{_bindir}/shfm install install-filemanager1

%files
%license LICENSE llama-go.LICENSE llama.cpp.LICENSE
%doc README.md
# Lets shfm connect to NFS exports that require a privileged source port
%caps(cap_net_bind_service=ep) %{_bindir}/shfm
%{_libdir}/gio/modules/libshfm-volume-monitor.so
%{_datadir}/applications/shfm.desktop
%{_datadir}/dbus-1/services/org.freedesktop.impl.portal.desktop.shfm.service
%{_datadir}/dbus-1/services/shfm.FileManager1.service
%{_datadir}/xdg-desktop-portal/portals/shfm.portal

%changelog
* Sat Oct 03 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.4.0-1
- Cloud storage sources: Google Drive (My Drive, shared drives, Shared
  with me), Dropbox and Microsoft OneDrive (My files with links to shared
  folders, and Shared for work or school accounts), authorized with OAuth
  in the browser.
- The source picker groups sources under Local, Remote and Cloud.
- Saved remote sources and cloud accounts can be removed from the source
  picker (x or Delete).
- The release archives have every feature too: semantic search, cloud
  storage, the GIO module and the desktop integration files.

* Thu Oct 01 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.3.1-1
- Without a graphical session, text and configuration files open in nano
  (or vim) in shfm's own terminal.
- Choosing the local drive holding the home folder opens the home folder.

* Wed Sep 30 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.3.0-1
- Optional Nerd Font icons (Ctrl+Alt+N): folders, standard user folders,
  links to folders and files, the trash, and files by type.
- Before turning them on, shfm checks that a Nerd Font is installed.

* Wed Sep 30 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.2.16-1
- About dialog (Ctrl+Alt+A): version, description, website and author.

* Tue Sep 29 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.2.15-1
- File associations dialog (o): the application opening each file type,
  by extension, following the freedesktop.org specifications.
- Files with no known extension are recognized by their content.
- Terminal=true applications open in a terminal.

* Mon Sep 28 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.2.14-1
- NFS: a denied mount from an unprivileged port suggests setcap.

* Mon Sep 28 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.2.13-1
- Packages for Fedora and derivatives.
- The Arch PKGBUILD is made from a template, with its source checksum.

* Mon Sep 28 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 0.2.12-1
- First RPM package.
