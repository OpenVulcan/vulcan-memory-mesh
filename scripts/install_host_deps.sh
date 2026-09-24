#!/usr/bin/env bash
# install_host_deps.sh installs pinned SQLite, LanceDB, and controller release assets for Unix hosts.
# install_host_deps.sh 用于为 Unix 宿主安装固定版本的 SQLite、LanceDB 与 controller release 资产。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
THIRD_PARTY_DIR="$PROJECT_DIR/third_party"
DEPS_DIR="$THIRD_PARTY_DIR/deps"
CHECKSUM_MANIFEST="$SCRIPT_DIR/host_deps_sha256.tsv"

# Pinned versions match the database crates embedded by vldb-controller v0.2.4.
# 固定版本与 vldb-controller v0.2.4 内嵌的数据库 crate 保持一致。
SQLITE_TAG="v0.1.7"
LANCEDB_TAG="v0.1.5"
CONTROLLER_TAG="v0.2.4"

# validate_checksum_manifest rejects missing, malformed, duplicate, and incomplete pinned archive records before any download or cache use.
# validate_checksum_manifest 在下载或使用缓存前拒绝缺失、格式错误、重复或不完整的固定压缩包摘要记录。
validate_checksum_manifest() {
    [[ -f "$CHECKSUM_MANIFEST" && ! -L "$CHECKSUM_MANIFEST" ]] || {
        echo "missing host dependency checksum manifest: $CHECKSUM_MANIFEST" >&2
        return 1
    }
    awk -F '\t' '
        BEGIN { count = 0; failed = 0 }
        /^#/ { next }
        NF == 0 { next }
        NF != 4 { failed = 1; next }
        length($1) == 0 || length($2) == 0 || length($3) == 0 || length($4) != 64 || $4 !~ /^[0-9A-Fa-f]+$/ {
            failed = 1
            next
        }
        { key = $1 SUBSEP $2 SUBSEP $3; if (++seen[key] != 1) failed = 1; count++ }
        END { if (count != 15) failed = 1; exit failed ? 1 : 0 }
    ' "$CHECKSUM_MANIFEST"
}

# pinned_archive_hash resolves one exact repository, tag, and asset tuple from the checked-in manifest.
# pinned_archive_hash 从仓库内固定清单解析唯一的仓库、标签和资产组合摘要。
pinned_archive_hash() {
    local repo="$1"
    local tag="$2"
    local asset_name="$3"
    awk -F '\t' -v expected_repo="$repo" -v expected_tag="$tag" -v expected_asset="$asset_name" '
        BEGIN { count = 0; value = "" }
        /^#/ || NF == 0 { next }
        NF == 4 && $1 == expected_repo && $2 == expected_tag && $3 == expected_asset { count++; value = tolower($4) }
        END { if (count != 1) exit 1; print value }
    ' "$CHECKSUM_MANIFEST"
}

# sha256_file prints a lowercase SHA256 digest on Linux or macOS.
# sha256_file 在 Linux 或 macOS 上输出小写 SHA256 摘要。
sha256_file() {
    local path="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$path" | awk '{print tolower($1)}'
        return
    fi
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$path" | awk '{print tolower($1)}'
        return
    fi
    echo "sha256sum or shasum is required / 需要 sha256sum 或 shasum" >&2
    return 1
}

# resolve_target maps the current Unix platform to the release target triple and archive extension.
# resolve_target 把当前 Unix 平台映射到 release target triple 与压缩包扩展名。
resolve_target() {
    local os_name
    local arch_name
    os_name="$(uname -s)"
    arch_name="$(uname -m)"
    case "$arch_name" in
        x86_64|amd64) arch_name="x86_64" ;;
        arm64|aarch64) arch_name="aarch64" ;;
        *) echo "unsupported architecture: $arch_name" >&2; return 1 ;;
    esac
    case "$os_name" in
        Linux) printf '%s|%s\n' "${arch_name}-unknown-linux-gnu" ".tar.gz" ;;
        Darwin) printf '%s|%s\n' "${arch_name}-apple-darwin" ".tar.gz" ;;
        *) echo "unsupported Unix platform: $os_name" >&2; return 1 ;;
    esac
}

# install_dependency downloads, verifies, extracts, and records one pinned release asset.
# install_dependency 下载、校验、解压并记录一个固定版本 release 资产。
install_dependency() {
    local kind="$1"
    local repo="$2"
    local tag="$3"
    local asset_prefix="$4"
    local installed_name="$5"
    local target="$6"
    local archive_ext="$7"
    local target_dir="$THIRD_PARTY_DIR/${kind}"
    local asset_name="${asset_prefix}-${tag}-${target}${archive_ext}"
    local marker_file="$target_dir/.installed-${tag}-${target}"
    local installed_path="$DEPS_DIR/$installed_name"
    local expected_archive_hash
    if ! expected_archive_hash="$(pinned_archive_hash "$repo" "$tag" "$asset_name")"; then
        echo "missing or duplicate pinned SHA256 for $repo $tag $asset_name" >&2
        return 1
    fi

    mkdir -p "$target_dir" "$DEPS_DIR"
    local temp_dir
    temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/vmm-${kind}.XXXXXX")"
    trap 'rm -rf "$temp_dir"' EXIT
    local archive_path="$temp_dir/$asset_name"
    local local_archive
    local_archive="$(find "$THIRD_PARTY_DIR" -type f -name "$asset_name" -print -quit)"
    if [[ -n "$local_archive" ]]; then
        # A local cache is accepted only after it matches the repository-pinned archive digest; sidecar files are never trusted.
        # 本地缓存只有在匹配仓库固定压缩包摘要后才接受，绝不信任旁路 .sha256 文件。
        cp "$local_archive" "$archive_path"
    else
        local release_base="https://github.com/${repo}/releases/download/${tag}"
        echo "==> Downloading $kind dependency: $asset_name"
        curl -fsSL "$release_base/$asset_name" -o "$archive_path"
    fi

    local actual_archive_hash
    actual_archive_hash="$(sha256_file "$archive_path")"
    if [[ "$expected_archive_hash" != "$actual_archive_hash" ]]; then
        echo "$kind archive checksum mismatch: $asset_name" >&2
        return 1
    fi

    # Retain the verified archive in the dependency cache so later runs can revalidate trusted bytes without relying on a marker.
    # 将已验证压缩包保留在依赖缓存中，使后续运行可以重新校验可信字节，而不依赖 marker。
    local archive_cache_path="$target_dir/$asset_name"
    local archive_cache_tmp="$archive_cache_path.tmp.$$"
    cp "$archive_path" "$archive_cache_tmp"
    mv -f "$archive_cache_tmp" "$archive_cache_path"

    if [[ "$archive_ext" == ".zip" ]]; then
        unzip -q "$archive_path" -d "$temp_dir/extracted"
    else
        mkdir -p "$temp_dir/extracted"
        tar -xzf "$archive_path" -C "$temp_dir/extracted"
    fi
    local extracted_path
    extracted_path="$(find "$temp_dir/extracted" -type f -name "$installed_name" -print -quit)"
    if [[ -z "$extracted_path" ]]; then
        echo "installed artifact not found after extracting $asset_name: $installed_name" >&2
        return 1
    fi

    # Compare an existing installed file with the freshly extracted trusted artifact before allowing cache reuse.
    # 在允许复用缓存前，把已安装文件与刚从可信压缩包提取的文件进行比对。
    local trusted_installed_hash
    trusted_installed_hash="$(sha256_file "$extracted_path")"
    if [[ -f "$installed_path" ]]; then
        local actual_installed_hash
        actual_installed_hash="$(sha256_file "$installed_path")"
        if [[ "$actual_installed_hash" == "$trusted_installed_hash" ]]; then
            find "$target_dir" -maxdepth 1 -type f -name '.installed-*' -delete
            local marker_tmp="$marker_file.tmp.$$"
            printf '%s\n%s\n' "$expected_archive_hash" "$trusted_installed_hash" > "$marker_tmp"
            mv -f "$marker_tmp" "$marker_file"
            rm -rf "$temp_dir"
            trap - EXIT
            echo "==> $kind dependency already installed and verified ($asset_name)."
            return
        fi
    fi

    cp "$extracted_path" "$installed_path"
    chmod +x "$installed_path" 2>/dev/null || true
    find "$target_dir" -maxdepth 1 -type f -name '.installed-*' -delete
    local installed_hash="$trusted_installed_hash"
    local marker_tmp="$marker_file.tmp.$$"
    printf '%s\n%s\n' "$expected_archive_hash" "$installed_hash" > "$marker_tmp"
    mv -f "$marker_tmp" "$marker_file"
    rm -rf "$temp_dir"
    trap - EXIT
    echo "==> $kind dependency installed successfully."
}

validate_checksum_manifest

IFS='|' read -r TARGET ARCHIVE_EXT <<< "$(resolve_target)"
if [[ "$ARCHIVE_EXT" == ".zip" ]]; then
    SQLITE_LIBRARY="vldb_sqlite.dll"
    LANCEDB_LIBRARY="vldb_lancedb.dll"
    CONTROLLER_BINARY="vldb-controller.exe"
elif [[ "$TARGET" == *"apple-darwin" ]]; then
    SQLITE_LIBRARY="libvldb_sqlite.dylib"
    LANCEDB_LIBRARY="libvldb_lancedb.dylib"
    CONTROLLER_BINARY="vldb-controller"
else
    SQLITE_LIBRARY="libvldb_sqlite.so"
    LANCEDB_LIBRARY="libvldb_lancedb.so"
    CONTROLLER_BINARY="vldb-controller"
fi

install_dependency "vldb_sqlite" "OpenVulcan/vldb-sqlite" "$SQLITE_TAG" "vldb-sqlite-lib" "$SQLITE_LIBRARY" "$TARGET" "$ARCHIVE_EXT"
install_dependency "vldb_lancedb" "OpenVulcan/vldb-lancedb" "$LANCEDB_TAG" "vldb-lancedb-lib" "$LANCEDB_LIBRARY" "$TARGET" "$ARCHIVE_EXT"
install_dependency "vldb_controller" "OpenVulcan/vldb-controller" "$CONTROLLER_TAG" "vldb-controller" "$CONTROLLER_BINARY" "$TARGET" "$ARCHIVE_EXT"
