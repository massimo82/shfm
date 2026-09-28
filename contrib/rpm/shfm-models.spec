# The models for shfm's semantic search, in /usr/share/shfm/models, one
# package or the other; models in ~/.cache/shfm/models come first, role by
# role:
#
#   shfm-models     Qwen3-Embedding-0.6B (Q8_0) and Qwen3-Reranker-0.6B
#                   (Q4_K_M), about 1 GB, included
#   shfm-models-4b  Qwen3-Embedding-4B (Q8_0) and the reranker, about 4.7 GB:
#                   too large for a release asset, downloaded from Hugging
#                   Face when installed, checksums checked, and removed with
#                   the package
#
#   spectool -g -R contrib/rpm/shfm-models.spec
#   rpmbuild -bb contrib/rpm/shfm-models.spec
#
# Its version is the models', not shfm's. contrib/debian-models/models and
# contrib/arch/shfm-models* pin the same files: keep them in step.

%global models %{_datadir}/shfm/models
%global emb4b Qwen3-Embedding-4B-Q8_0.gguf
%global emb4b_sha256 b60ae5ce2dd6a0b77f82cadf21def1f310a3e10cde380ad0081b07a9d416949d
%global emb4b_url https://huggingface.co/Qwen/Qwen3-Embedding-4B-GGUF/resolve/main/%{emb4b}
%global rer Qwen3-Reranker-0.6B-Q4_K_M.gguf
%global rer_sha256 c04f5f5657c52e04538c455e8c62817db3d3b795b39e9f547f8581510445f075
%global rer_url https://huggingface.co/Voodisss/Qwen3-Reranker-0.6B-GGUF-llama_cpp/resolve/main/%{rer}

# The weights hardly compress
%global _binary_payload w3T0.zstdio

Name:           shfm-models
Version:        1
Release:        1%{?dist}
Summary:        Embedding (Qwen3-Embedding-0.6B) and reranker models for shfm's semantic search
License:        Apache-2.0
URL:            https://github.com/massimo82/shfm
Source0:        https://huggingface.co/Qwen/Qwen3-Embedding-0.6B-GGUF/resolve/main/Qwen3-Embedding-0.6B-Q8_0.gguf
Source1:        %{rer_url}
BuildArch:      noarch

Requires:       shfm > 0.2.10
Conflicts:      shfm-models-4b

%description
Qwen3-Embedding-0.6B (Q8_0) and Qwen3-Reranker-0.6B (Q4_K_M), about 1 GB, in
/usr/share/shfm/models, for shfm's semantic search on file contents.
shfm-models-4b has the larger, better embedding model instead. Models in
~/.cache/shfm/models come first, role by role.

%package -n shfm-models-4b
Summary:        Embedding (Qwen3-Embedding-4B) and reranker models for shfm's semantic search
Requires:       shfm > 0.2.10
Requires:       curl
Requires(post): curl
Requires(post): coreutils
Conflicts:      shfm-models

%description -n shfm-models-4b
Downloads Qwen3-Embedding-4B (Q8_0) and Qwen3-Reranker-0.6B (Q4_K_M) from
Hugging Face when installed, about 4.7 GB, checks their checksums and puts
them in /usr/share/shfm/models, for shfm's semantic search on file contents:
better retrieval than shfm-models, slower queries. Removing the package
removes them. Models in ~/.cache/shfm/models come first, role by role.

%prep
echo "06507c7b42688469c4e7298b0a1e16deff06caf291cf0a5b278c308249c3e439  %{SOURCE0}" | sha256sum -c
echo "%{rer_sha256}  %{SOURCE1}" | sha256sum -c

%build

%install
install -Dm644 -t %{buildroot}%{models} %{SOURCE0} %{SOURCE1}

%files
%dir %{_datadir}/shfm
%dir %{models}
%{models}/Qwen3-Embedding-0.6B-Q8_0.gguf
%{models}/%{rer}

# The models aren't the package's files: downloaded here, resuming what a
# failed attempt left, and removed when the package is. A failed download
# (no network...) fails the installation, and reinstalling tries again.
%post -n shfm-models-4b
set -e
download() { # file sha256 url
	if [ -f "%{models}/$1" ] && echo "$2  %{models}/$1" | sha256sum -c --status; then
		return 0
	fi
	echo "shfm-models-4b: downloading $1 from Hugging Face..."
	curl -fL --retry 3 -C - -o "%{models}/$1.part" "$3"
	if ! echo "$2  %{models}/$1.part" | sha256sum -c --status; then
		rm -f "%{models}/$1.part"
		echo "shfm-models-4b: $1 doesn't match its checksum" >&2
		return 1
	fi
	chmod 644 "%{models}/$1.part"
	mv "%{models}/$1.part" "%{models}/$1"
}
mkdir -p %{models}
download %{emb4b} %{emb4b_sha256} %{emb4b_url}
download %{rer} %{rer_sha256} %{rer_url}

%postun -n shfm-models-4b
# $1 is 0 on removal, not on upgrade
if [ "$1" -eq 0 ]; then
	rm -f %{models}/%{emb4b} %{models}/%{emb4b}.part %{models}/%{rer} %{models}/%{rer}.part
	rmdir --ignore-fail-on-non-empty %{models} %{_datadir}/shfm 2>/dev/null || true
fi

%files -n shfm-models-4b

%changelog
* Mon Sep 28 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com> - 1-1
- First models packages: Qwen3-Embedding-0.6B or 4B, Qwen3-Reranker-0.6B.
