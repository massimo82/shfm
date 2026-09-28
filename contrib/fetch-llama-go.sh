#!/bin/sh
# Fetches what the semantic (content) search build needs into
# third_party/llama-go: upstream llama-go, pinned to the commit shfm's patches
# target, merged in without overwriting the patched files, and llama.cpp at
# the commit llama-go's submodule points at, in place of the submodule. The
# same as step 1 of README "Full build", from checksummed archives instead of
# git. Run from anywhere; does nothing if llama.cpp is already there.
#
# contrib/arch/shfm/PKGBUILD and contrib/rpm/shfm.spec pin the same commits:
# keep them in step.

set -eu

LLAMA_GO=992bbf8cfdc7484d4b3d724fa9133c5a87d6310d
LLAMA_GO_SHA256=6fd78f62efb2bd36bbd370f1be3fdda25b33cb7ee8076a9b5296fea4e513339c
LLAMA_CPP=90c26fcd4b2114b4aa39d09d69318cb8f438d27a
LLAMA_CPP_SHA256=f679a75f99b6d2ca2efd25596c2d856745eec2a42c43cfdd7e00a373d40c4357

dest=$(cd "$(dirname "$0")/.." && pwd)/third_party/llama-go
if [ -f "$dest/llama.cpp/CMakeLists.txt" ]; then
	echo "llama.cpp already in $dest"
	exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # url sha256 file
	curl -fsSL -o "$tmp/$3" "$1"
	echo "$2  $tmp/$3" | sha256sum -c --quiet
	tar -xzf "$tmp/$3" -C "$tmp"
}
fetch "https://github.com/tcpipuk/llama-go/archive/$LLAMA_GO.tar.gz" "$LLAMA_GO_SHA256" llama-go.tar.gz
fetch "https://github.com/ggml-org/llama.cpp/archive/$LLAMA_CPP.tar.gz" "$LLAMA_CPP_SHA256" llama.cpp.tar.gz

cp -an "$tmp/llama-go-$LLAMA_GO"/. "$dest"/
rm -rf "$dest/llama.cpp"
mv "$tmp/llama.cpp-$LLAMA_CPP" "$dest/llama.cpp"
echo "llama-go and llama.cpp fetched into $dest"
