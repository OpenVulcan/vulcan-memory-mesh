#!/usr/bin/env bash
# install_host_deps.sh installs pinned SQLite, LanceDB, and controller release assets for Unix hosts.
# install_host_deps.sh 用于为 Unix 宿主安装固定版本的 SQLite、LanceDB 与 controller release 资产。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
THIRD_PARTY_DIR="$PROJECT_DIR/third_party"
DEPS_DIR="$THIRD_PARTY_DIR/deps"

# Pinned versions match the database crates embedded by vldb-controller v0.2.3.
# 固定版本与 vldb-controller v0.2.3 内嵌的数据库 crate 保持一致。
SQLITE_TAG="v0.1.6"
LANCEDB_TAG="v0.1.5"
CONTROLLER_TAG="v0.2.3"

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

    mkdir -p "$target_dir" "$DEPS_DIR"
    if [[ -f "$marker_file" && -f "$installed_path" ]]; then
        local expected_installed_hash
        local actual_installed_hash
        expected_installed_hash="$(tr -d '[:space:]' < "$marker_file" | tr '[:upper:]' '[:lower:]')"
        actual_installed_hash="$(sha256_file "$installed_path")"
        if [[ -n "$expected_installed_hash" && "$expected_installed_hash" == "$actual_installed_hash" ]]; then
            echo "==> $kind dependency already installed and verified ($asset_name)."
            return
        fi
    fi

    local temp_dir
    temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/vmm-${kind}.XXXXXX")"
    trap 'rm -rf "$temp_dir"' EXIT
    local archive_path="$temp_dir/$asset_name"
    local checksum_path="$archive_path.sha256"
    local local_archive
    local_archive="$(find "$THIRD_PARTY_DIR" -type f -name "$asset_name" -print -quit)"
    if [[ -n "$local_archive" ]]; then
        if [[ ! -f "$local_archive.sha256" ]]; then
            echo "local dependency archive requires checksum sidecar: $local_archive.sha256" >&2
            return 1
        fi
        cp "$local_archive" "$archive_path"
        cp "$local_archive.sha256" "$checksum_path"
    else
        local release_base="https://github.com/${repo}/releases/download/${tag}"
        echo "==> Downloading $kind dependency: $asset_name"
        curl -fsSL "$release_base/$asset_name" -o "$archive_path"
        curl -fsSL "$release_base/$asset_name.sha256" -o "$checksum_path"
    fi

    local expected_archive_hash
    local actual_archive_hash
    expected_archive_hash="$(awk '{print tolower($1); exit}' "$checksum_path")"
    actual_archive_hash="$(sha256_file "$archive_path")"
    if [[ -z "$expected_archive_hash" || "$expected_archive_hash" != "$actual_archive_hash" ]]; then
        echo "$kind archive checksum mismatch: $asset_name" >&2
        return 1
    fi

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
    cp "$extracted_path" "$installed_path"
    chmod +x "$installed_path" 2>/dev/null || true
    find "$target_dir" -maxdepth 1 -type f -name '.installed-*' -delete
    sha256_file "$installed_path" > "$marker_file"
    rm -rf "$temp_dir"
    trap - EXIT
    echo "==> $kind dependency installed successfully."
}

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
