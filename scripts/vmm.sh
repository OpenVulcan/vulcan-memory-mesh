#!/usr/bin/env bash
# scripts/vmm.sh builds the standalone VMM package and owns only declared output artifacts.
# scripts/vmm.sh 用于构建独立 VMM 交付包，并且只管理明确声明的构建产物。
set -euo pipefail

ACTION="${1:-build}"
if [[ $# -gt 0 ]]; then shift; fi
FORWARD_ARGS=("$@")

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="$ROOT_DIR/output"
BIN_DIR="$OUTPUT_DIR/bin"
LIB_DIR="$OUTPUT_DIR/libs"
CONFIG_DIR="$OUTPUT_DIR/configs"
DATABASE_DIR="$OUTPUT_DIR/database"
CONFIG_OWNERSHIP_FILE="$OUTPUT_DIR/.vmm-config-owned"
EXE_PATH="$BIN_DIR/vmm-local"
MIGRATE_EXE_PATH="$BIN_DIR/vmm-migrate"
TESTER_EXE_PATH="$BIN_DIR/vmm-pii-tester"
THIRD_PARTY_DEPS_DIR="$ROOT_DIR/third_party/deps"
NATIVE_DEPS_ROOT="$THIRD_PARTY_DEPS_DIR/native_lancedb"
NATIVE_VALIDATOR_SCRIPT="$SCRIPT_DIR/validate_native_artifacts.sh"
HOST_DEPS_CHECKSUM_MANIFEST="$SCRIPT_DIR/host_deps_sha256.tsv"

# Native support paths are a fixed allowlist copied beside the native library.
# 原生支持路径使用固定白名单并复制到动态库旁。
NATIVE_SUPPORT_ARTIFACT_PATHS=(
    native_lancedb-support/include/vmm_lancedb.h
    native_lancedb-support/licenses/THIRD_PARTY_NOTICES.txt
    native_lancedb-support/licenses/lancedb-0.39.0-license-metadata.txt
    native_lancedb-support/licenses/lancedb-0.39.0-LICENSE
    native_lancedb-support/licenses/jieba-rs-0.10.4-dictionary-notice.txt
    native_lancedb-support/licenses/jieba-rs-0.10.4-LICENSE
    native_lancedb-support/licenses/gse-1.0.2-LICENSE
    native_lancedb-support/licenses/gse-1.0.2-embedded-dictionary-notice.txt
    native_lancedb-support/licenses/cedar-0.30.0-LICENSE
    native_lancedb-support/licenses/modernc-sqlite-1.59.0-LICENSE
    native_lancedb-support/licenses/modernc-libc-1.75.7-LICENSE
    native_lancedb-support/licenses/modernc-mathutil-1.7.1-LICENSE
    native_lancedb-support/licenses/modernc-memory-1.12.1-LICENSE
    native_lancedb-support/licenses/go-humanize-1.0.1-LICENSE
    native_lancedb-support/licenses/go-isatty-0.0.24-LICENSE
    native_lancedb-support/licenses/go-strftime-1.0.0-LICENSE
    native_lancedb-support/licenses/bigfft-20230129092748-LICENSE
    native_lancedb-support/licenses/x-sys-0.47.0-LICENSE
)

# resolve_build_profile accepts only the documented standard and release forms.
# resolve_build_profile 仅接受文档规定的 standard 与 release 形式。
lowercase() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

resolve_build_profile() {
    if [[ $# -eq 0 ]]; then printf '%s\n' standard; return; fi
    if [[ $# -eq 1 && "$(lowercase "$1")" == "release" ]]; then printf '%s\n' release; return; fi
    echo "unsupported build arguments: $*. Use 'build' or 'build release'." >&2
    return 1
}

# resolve_storage_profile validates the build-only dependency profile before output changes.
# resolve_storage_profile 在修改 output 前校验仅用于构建的依赖配置。
resolve_storage_profile() {
    local profile="${VMM_BUILD_STORAGE_PROFILE:-legacy}"
    profile="$(lowercase "$profile")"
    case "$profile" in legacy|native|all) printf '%s\n' "$profile" ;; *) echo "unsupported VMM_BUILD_STORAGE_PROFILE: $profile" >&2; return 1 ;; esac
}

# assert_output_dir rejects an output symlink so cleanup cannot escape the formal package root.
# assert_output_dir 拒绝 output 符号链接，确保清理不会逃逸正式交付根目录。
assert_output_dir() {
    if [[ -e "$OUTPUT_DIR" || -L "$OUTPUT_DIR" ]]; then
        [[ -d "$OUTPUT_DIR" && ! -L "$OUTPUT_DIR" ]] || { echo "output must be a real directory: $OUTPUT_DIR" >&2; return 1; }
    else
        mkdir -p "$OUTPUT_DIR"
    fi
}

# assert_output_path checks every existing component of one output-relative path.
# assert_output_path 校验一个 output 相对路径的所有已存在组成部分。
assert_output_path() {
    local path="$1"
    assert_output_dir
    case "$path" in "$OUTPUT_DIR"/*) ;; *) echo "refusing path outside output: $path" >&2; return 1 ;; esac
    local relative="${path#"$OUTPUT_DIR"/}"
    [[ "$relative" != "" && "$relative" != /* && "$relative" != *"/../"* && "$relative" != ../* && "$relative" != */.. && "$relative" != *"//"* ]] || { echo "invalid output path: $path" >&2; return 1; }
    local current="$OUTPUT_DIR" part
    IFS='/' read -r -a parts <<< "$relative"
    for part in "${parts[@]}"; do
        [[ "$part" != "" && "$part" != "." && "$part" != ".." ]] || { echo "invalid output path: $path" >&2; return 1; }
        current="$current/$part"
        if [[ -L "$current" ]]; then echo "refusing reparse/symlink in output: $current" >&2; return 1; fi
    done
}

# sha256_file emits a lowercase SHA-256 digest for one regular file.
# sha256_file 输出一个普通文件的小写 SHA-256 摘要。
sha256_file() {
    local path="$1"
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$path" | awk '{print tolower($1)}'; return; fi
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$path" | awk '{print tolower($1)}'; return; fi
    echo "sha256sum or shasum is required" >&2
    return 1
}

# resolve_host_artifacts returns the legacy library and controller names for this Unix host.
# resolve_host_artifacts 返回当前 Unix 宿主的 legacy 库和 controller 文件名。
resolve_host_artifacts() {
    case "$(uname -s)" in
        Darwin) printf '%s|%s|%s\n' libvldb_sqlite.dylib libvldb_lancedb.dylib vldb-controller ;;
        Linux) printf '%s|%s|%s\n' libvldb_sqlite.so libvldb_lancedb.so vldb-controller ;;
        *) echo "unsupported platform for VMM packaging: $(uname -s)" >&2; return 1 ;;
    esac
}

# native_target maps the Unix host to the Rust target recorded in native manifests.
# native_target 将 Unix 宿主映射到原生清单中记录的 Rust target。
native_target() {
    local arch
    arch="$(uname -m)"
    case "$arch" in x86_64|amd64) arch=x86_64 ;; arm64|aarch64) arch=aarch64 ;; *) echo "unsupported architecture: $arch" >&2; return 1 ;; esac
    case "$(uname -s)" in Linux) printf '%s-unknown-linux-gnu\n' "$arch" ;; Darwin) printf '%s-apple-darwin\n' "$arch" ;; *) echo "unsupported Unix platform" >&2; return 1 ;; esac
}

# native_library_name returns the exact native ABI filename for the host.
# native_library_name 返回宿主对应的精确原生 ABI 文件名。
native_library_name() {
    case "$(uname -s)" in Linux) printf '%s\n' libvmm_lancedb_native.so ;; Darwin) printf '%s\n' libvmm_lancedb_native.dylib ;; *) echo "unsupported Unix platform" >&2; return 1 ;; esac
}

# pinned_host_archive_hash resolves one exact archive digest from the checked-in manifest.
# pinned_host_archive_hash 从仓库内固定清单解析唯一压缩包摘要。
pinned_host_archive_hash() {
    local repo="$1" tag="$2" asset_name="$3" value
    [[ -f "$HOST_DEPS_CHECKSUM_MANIFEST" && ! -L "$HOST_DEPS_CHECKSUM_MANIFEST" ]] || {
        echo "missing host dependency checksum manifest: $HOST_DEPS_CHECKSUM_MANIFEST" >&2
        return 1
    }
    value="$(awk -F '\t' -v expected_repo="$repo" -v expected_tag="$tag" -v expected_asset="$asset_name" '
        BEGIN { total = 0; matches = 0; failed = 0; value = "" }
        /^#/ || NF == 0 { next }
        NF != 4 || length($1) == 0 || length($2) == 0 || length($3) == 0 || length($4) != 64 || $4 !~ /^[0-9A-Fa-f]+$/ {
            failed = 1
            next
        }
        { key = $1 SUBSEP $2 SUBSEP $3; if (++seen[key] != 1) failed = 1; total++ }
        $1 == expected_repo && $2 == expected_tag && $3 == expected_asset {
            matches++
            value = tolower($4)
        }
        END {
            if (failed || total != 15 || matches != 1 || length(value) != 64 || value !~ /^[0-9a-f]+$/) { exit 1 }
            print value
        }
    ' "$HOST_DEPS_CHECKSUM_MANIFEST")" || {
        echo "missing or duplicate pinned SHA256 for $repo $tag $asset_name" >&2
        return 1
    }
    printf '%s\n' "$value"
}

# host_dependency_release_info maps one installed artifact to its immutable official archive.
# host_dependency_release_info 将一个已安装产物映射到不可变的官方压缩包。
host_dependency_release_info() {
    local kind="$1" target="$2"
    case "$kind" in
        sqlite) printf '%s|%s|%s\n' OpenVulcan/vldb-sqlite v0.1.6 "vldb-sqlite-lib-v0.1.6-${target}.tar.gz" ;;
        lancedb) printf '%s|%s|%s\n' OpenVulcan/vldb-lancedb v0.1.5 "vldb-lancedb-lib-v0.1.5-${target}.tar.gz" ;;
        controller) printf '%s|%s|%s\n' OpenVulcan/vldb-controller v0.2.3 "vldb-controller-v0.2.3-${target}.tar.gz" ;;
        *) echo "unknown dependency kind: $kind" >&2; return 1 ;;
    esac
}

# verified_host_dependency revalidates the official archive and extracted artifact before packaging.
# verified_host_dependency 在打包前重新校验官方压缩包及其解压产物。
verified_host_dependency() {
    local kind="$1" file_name="$2" source_path="$THIRD_PARTY_DEPS_DIR/$file_name" marker_dir marker_count marker
    [[ -f "$source_path" && ! -L "$source_path" ]] || { echo "missing or unsafe host dependency: $source_path; run scripts/install_host_deps.sh first" >&2; return 1; }
    case "$kind" in sqlite) marker_dir="$ROOT_DIR/third_party/vldb_sqlite" ;; lancedb) marker_dir="$ROOT_DIR/third_party/vldb_lancedb" ;; controller) marker_dir="$ROOT_DIR/third_party/vldb_controller" ;; *) echo "unknown dependency kind: $kind" >&2; return 1 ;; esac
    [[ -d "$marker_dir" && ! -L "$marker_dir" ]] || { echo "host dependency cache must be a real directory: $marker_dir" >&2; return 1; }

    local target release_info repo tag archive_name archive_path expected_archive_hash actual_archive_hash
    target="$(native_target)"
    release_info="$(host_dependency_release_info "$kind" "$target")"
    IFS='|' read -r repo tag archive_name <<< "$release_info"
    archive_path="$marker_dir/$archive_name"
    [[ -f "$archive_path" && ! -L "$archive_path" ]] || { echo "missing trusted host dependency archive: $archive_path; rerun scripts/install_host_deps.sh" >&2; return 1; }
    expected_archive_hash="$(pinned_host_archive_hash "$repo" "$tag" "$archive_name")" || return 1
    actual_archive_hash="$(sha256_file "$archive_path")"
    [[ "$actual_archive_hash" == "$expected_archive_hash" ]] || { echo "host dependency archive checksum mismatch: $archive_path" >&2; return 1; }

    local markers=()
    while IFS= read -r marker_path; do markers+=("$marker_path"); done < <(find "$marker_dir" -maxdepth 1 -type f -name '.installed-*' -print 2>/dev/null | sort)
    marker_count="${#markers[@]}"
    [[ "$marker_count" -eq 1 ]] || { echo "expected exactly one installed marker for $kind under $marker_dir" >&2; return 1; }
    marker="${markers[0]}"
    [[ ! -L "$marker" ]] || { echo "host dependency marker must not be a symlink: $marker" >&2; return 1; }
    [[ "$(basename "$marker")" == ".installed-${tag}-${target}" ]] || { echo "host dependency marker identity mismatch: $marker" >&2; return 1; }
    [[ "$(wc -l < "$marker" | tr -d '[:space:]')" == 2 ]] || { echo "host dependency marker must contain exactly two lines: $marker" >&2; return 1; }
    local marker_archive_hash marker_installed_hash
    marker_archive_hash="$(sed -n '1p' "$marker" | tr -d '\r')"
    marker_installed_hash="$(sed -n '2p' "$marker" | tr -d '\r')"
    [[ "$marker_archive_hash" =~ ^[0-9a-f]{64}$ && "$marker_installed_hash" =~ ^[0-9a-f]{64}$ ]] || { echo "host dependency marker must contain two lowercase SHA256 values: $marker" >&2; return 1; }
    [[ "$marker_archive_hash" == "$expected_archive_hash" ]] || { echo "host dependency marker archive checksum mismatch: $marker" >&2; return 1; }

    # Re-extract the fixed archive so a jointly forged marker and installed file cannot pass the package gate.
    # 重新解压固定压缩包，防止伪造 marker 与已安装文件共同绕过打包校验。
    local verify_dir trusted_path trusted_installed_hash actual_installed_hash
    verify_dir="$(mktemp -d "${TMPDIR:-/tmp}/vmm-host-verify.XXXXXX")"
    if ! tar -xzf "$archive_path" -C "$verify_dir"; then
        rm -rf "$verify_dir"
        echo "failed to extract trusted host dependency archive: $archive_path" >&2
        return 1
    fi
    local trusted_paths=()
    while IFS= read -r trusted_candidate; do trusted_paths+=("$trusted_candidate"); done < <(find "$verify_dir" -type f -name "$file_name" -print 2>/dev/null)
    if [[ "${#trusted_paths[@]}" -ne 1 ]]; then
        rm -rf "$verify_dir"
        echo "trusted host dependency artifact identity mismatch in $archive_path: $file_name" >&2
        return 1
    fi
    trusted_path="${trusted_paths[0]}"
    if ! trusted_installed_hash="$(sha256_file "$trusted_path")"; then
        rm -rf "$verify_dir"
        return 1
    fi
    if ! actual_installed_hash="$(sha256_file "$source_path")"; then
        rm -rf "$verify_dir"
        return 1
    fi
    rm -rf "$verify_dir"
    [[ "$actual_installed_hash" == "$trusted_installed_hash" ]] || { echo "host dependency artifact differs from trusted archive: $source_path" >&2; return 1; }
    [[ "$marker_installed_hash" == "$actual_installed_hash" && "$marker_installed_hash" == "$trusted_installed_hash" ]] || { echo "host dependency marker installed checksum mismatch: $marker" >&2; return 1; }
    printf '%s\n' "$source_path"
}

# resolve_native_artifact follows current.json exactly and validates its manifest and library.
# resolve_native_artifact 严格读取 current.json，并校验其清单和动态库。
resolve_native_artifact() {
    local target="$1" target_root="$NATIVE_DEPS_ROOT/$target" current_path="$NATIVE_DEPS_ROOT/$target/current.json"
    [[ -d "$target_root" && ! -L "$target_root" ]] || { echo "native dependency target directory must be a real directory: $target_root" >&2; return 1; }
    [[ -f "$current_path" ]] || { echo "missing native dependency selection: $current_path; run scripts/build_native_deps.sh first" >&2; return 1; }
    [[ ! -L "$current_path" ]] || { echo "native current.json must not be a symlink: $current_path" >&2; return 1; }
    [[ -x "$NATIVE_VALIDATOR_SCRIPT" || -f "$NATIVE_VALIDATOR_SCRIPT" ]] || { echo "missing native artifact validator: $NATIVE_VALIDATOR_SCRIPT" >&2; return 1; }
    local current_values
    current_values="$(python3 - "$current_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
for key in ("schema_version", "target", "source_digest", "manifest_path", "library_file"):
    print(json.dumps(value.get(key, ""), ensure_ascii=False))
PY
)"
    local current_fields=()
    while IFS= read -r current_field; do current_fields+=("$current_field"); done <<< "$current_values"
    [[ "${#current_fields[@]}" -eq 5 ]] || { echo "native current.json fields are incomplete" >&2; return 1; }
    local schema target_value source_digest manifest_relative current_library
    schema="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${current_fields[0]}")"
    target_value="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${current_fields[1]}")"
    source_digest="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${current_fields[2]}")"
    manifest_relative="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${current_fields[3]}")"
    current_library="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${current_fields[4]}")"
    [[ "$schema" == 1 && "$target_value" == "$target" && "$source_digest" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "native current.json schema, target, or source digest mismatch" >&2; return 1; }
    [[ "$manifest_relative" == "$source_digest/manifest.json" ]] || { echo "native current.json manifest path mismatch" >&2; return 1; }
    local manifest_path="$target_root/$manifest_relative"
    [[ -d "$(dirname "$manifest_path")" && ! -L "$(dirname "$manifest_path")" ]] || { echo "native manifest directory must be a real directory: $(dirname "$manifest_path")" >&2; return 1; }
    [[ -f "$manifest_path" ]] || { echo "missing native manifest: $manifest_path" >&2; return 1; }
    local manifest_values
    manifest_values="$(python3 - "$manifest_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
print(value.get("library_file", ""))
PY
)"
    [[ "$current_library" == "$manifest_values" ]] || { echo "native current.json library does not match manifest" >&2; return 1; }
    local library_path="$(dirname "$manifest_path")/$manifest_values"
    NATIVE_ARTIFACT_RESULT=()
    while IFS= read -r artifact_path; do NATIVE_ARTIFACT_RESULT+=("$artifact_path"); done < <(printf '%s\n' "$manifest_path" "$library_path" "$source_digest")
    "$NATIVE_VALIDATOR_SCRIPT" --manifest "$manifest_path" --library "$library_path" --target "$target" --expected-source-digest "$source_digest"
}

# source_identity emits the Git identity values embedded in ordinary Go binaries.
# source_identity 输出普通 Go 二进制中嵌入的 Git 身份值。
source_identity() {
    SOURCE_REVISION="$(git -C "$ROOT_DIR" rev-parse HEAD)"
    local line relative absolute
    {
        while IFS= read -r relative; do
            [[ -n "$relative" ]] || continue
            absolute="$ROOT_DIR/$relative"
            [[ -f "$absolute" ]] || continue
            printf '%s\0%s\n' "${relative//\\//}" "$(sha256_file "$absolute")"
        done < <(git -C "$ROOT_DIR" ls-files -co --exclude-standard | LC_ALL=C sort)
    } | sha256_file_from_stdin
}

# sha256_file_from_stdin hashes a generated source identity stream without creating a repository artifact.
# sha256_file_from_stdin 对生成的源码身份流计算摘要，不创建仓库产物。
sha256_file_from_stdin() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print tolower($1)}'; return; fi
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 | awk '{print tolower($1)}'; return; fi
    echo "sha256sum or shasum is required" >&2
    return 1
}

# sync_configs copies source files one by one and protects unowned output files.
# sync_configs 逐个复制源文件，并保护未声明拥有的 output 文件。
sync_configs() {
    local source_config="$ROOT_DIR/configs" previous_entry relative entry destination destination_parent source_hash destination_hash old_path temp_file
    [[ -d "$source_config" ]] || { echo "missing config source directory: $source_config" >&2; return 1; }
    assert_output_dir
    assert_output_path "$CONFIG_DIR"
    mkdir -p "$CONFIG_DIR"
    local previous_file=""
    if [[ -f "$CONFIG_OWNERSHIP_FILE" ]]; then assert_output_path "$CONFIG_OWNERSHIP_FILE"; previous_file="$CONFIG_OWNERSHIP_FILE"; fi
    temp_file="$(mktemp "${TMPDIR:-/tmp}/vmm-config-owned.XXXXXX")"
    while IFS= read -r source_file; do
        relative="${source_file#"$source_config"/}"
        entry="configs/${relative//\\//}"
        destination="$CONFIG_DIR/${relative//\\//}"
        destination_parent="$(dirname "$destination")"
        assert_output_path "$destination_parent"
        mkdir -p "$destination_parent"
        assert_output_path "$destination"
        if [[ -e "$destination" ]]; then
            if [[ -z "$previous_file" ]] || ! grep -Fqx "$entry" "$previous_file"; then
                source_hash="$(sha256_file "$source_file")"
                destination_hash="$(sha256_file "$destination")"
                [[ "$source_hash" == "$destination_hash" ]] || { rm -f "$temp_file"; echo "refusing to overwrite unowned config: $destination" >&2; return 1; }
            fi
        fi
        cp -f "$source_file" "$destination"
        printf '%s\n' "$entry" >> "$temp_file"
    done < <(find "$source_config" -type f -print | LC_ALL=C sort)
    if [[ -n "$previous_file" ]]; then
        while IFS= read -r previous_entry; do
            [[ -n "$previous_entry" ]] || continue
            if ! grep -Fqx "$previous_entry" "$temp_file"; then
                old_path="$OUTPUT_DIR/${previous_entry//\\//}"
                assert_output_path "$old_path"
                [[ ! -f "$old_path" ]] || rm -f "$old_path"
            fi
        done < "$previous_file"
    fi
    assert_output_path "$CONFIG_OWNERSHIP_FILE"
    sort -u "$temp_file" > "$CONFIG_OWNERSHIP_FILE"
    rm -f "$temp_file"
}

# remove_owned_configs deletes only files from the build ownership list and preserves user data.
# remove_owned_configs 只删除构建拥有清单中的文件，并保留用户数据。
remove_owned_configs() {
    [[ -f "$CONFIG_OWNERSHIP_FILE" ]] || return 0
    local entry path
    while IFS= read -r entry; do
        [[ -n "$entry" ]] || continue
        path="$OUTPUT_DIR/${entry//\\//}"
        assert_output_path "$path"
        [[ ! -f "$path" ]] || rm -f "$path"
    done < "$CONFIG_OWNERSHIP_FILE"
    assert_output_path "$CONFIG_OWNERSHIP_FILE"
    rm -f "$CONFIG_OWNERSHIP_FILE"
}

# remove_owned_artifact removes one known file after exact output-path validation.
# remove_owned_artifact 在精确校验 output 路径后删除一个已知文件。
remove_owned_artifact() {
    local path="$1"
    [[ -e "$path" || -L "$path" ]] || return 0
    assert_output_path "$path"
    [[ -f "$path" ]] || { echo "refusing to remove non-file build artifact: $path" >&2; return 1; }
    rm -f "$path"
}

# sync_host_dependencies copies only the selected, checksum-verified profile and removes known stale artifacts.
# sync_host_dependencies 只复制选定且已校验的配置，并删除已知过期产物。
sync_host_dependencies() {
    local profile="$1" sqlite_library lancedb_library controller_binary source target expected name
    IFS='|' read -r sqlite_library lancedb_library controller_binary <<< "$(resolve_host_artifacts)"
    assert_output_path "$LIB_DIR"
    mkdir -p "$LIB_DIR"
    local expected_names=()
    if [[ "$profile" == legacy || "$profile" == all ]]; then
        expected_names+=("$sqlite_library" "$lancedb_library")
        source="$(verified_host_dependency sqlite "$sqlite_library")"; assert_output_path "$LIB_DIR/$sqlite_library"; [[ ! -d "$LIB_DIR/$sqlite_library" ]] || { echo "refusing to overwrite a library directory: $LIB_DIR/$sqlite_library" >&2; return 1; }; cp -f "$source" "$LIB_DIR/$sqlite_library"
        source="$(verified_host_dependency lancedb "$lancedb_library")"; assert_output_path "$LIB_DIR/$lancedb_library"; [[ ! -d "$LIB_DIR/$lancedb_library" ]] || { echo "refusing to overwrite a library directory: $LIB_DIR/$lancedb_library" >&2; return 1; }; cp -f "$source" "$LIB_DIR/$lancedb_library"
    fi
    if [[ "$profile" == native || "$profile" == all ]]; then
        target="$(native_target)"
        resolve_native_artifact "$target"
        local manifest_path="${NATIVE_ARTIFACT_RESULT[0]}" library_path="${NATIVE_ARTIFACT_RESULT[1]}"
        name="$(native_library_name)"
        expected_names+=("$name" manifest.json)
        assert_output_path "$LIB_DIR/$name"
        assert_output_path "$LIB_DIR/manifest.json"
        [[ ! -d "$LIB_DIR/$name" && ! -d "$LIB_DIR/manifest.json" ]] || { echo "refusing to overwrite a native artifact directory" >&2; return 1; }
        cp -f "$library_path" "$LIB_DIR/$name"
        cp -f "$manifest_path" "$LIB_DIR/manifest.json"
        local support_path support_source support_destination support_parent
        for support_path in "${NATIVE_SUPPORT_ARTIFACT_PATHS[@]}"; do
            support_source="$(dirname "$manifest_path")/$support_path"
            [[ -f "$support_source" && ! -L "$support_source" ]] || { echo "native support artifact is missing or a symlink: $support_source" >&2; return 1; }
            support_destination="$LIB_DIR/$support_path"
            support_parent="$(dirname "$support_destination")"
            assert_output_path "$support_parent"
            mkdir -p "$support_parent"
            assert_output_path "$support_destination"
            [[ ! -d "$support_destination" ]] || { echo "refusing to overwrite a native support directory: $support_destination" >&2; return 1; }
            cp -f "$support_source" "$support_destination"
            expected_names+=("$support_path")
        done
        "$NATIVE_VALIDATOR_SCRIPT" --manifest "$LIB_DIR/manifest.json" --library "$LIB_DIR/$name" --target "$target" --expected-source-digest "${NATIVE_ARTIFACT_RESULT[2]}"
    fi
    local known_names=("$sqlite_library" "$lancedb_library" "$(native_library_name)" manifest.json "${NATIVE_SUPPORT_ARTIFACT_PATHS[@]}")
    for name in "${known_names[@]}"; do
        expected=0
        for target in "${expected_names[@]}"; do [[ "$name" == "$target" ]] && expected=1; done
        if [[ "$expected" -eq 0 ]]; then remove_owned_artifact "$LIB_DIR/$name"; fi
    done
}

# sync_controller_binary copies the verified controller only for legacy-compatible profiles.
# sync_controller_binary 仅在兼容 legacy 的配置中复制已校验 controller。
sync_controller_binary() {
    local controller_binary source
    IFS='|' read -r _ _ controller_binary <<< "$(resolve_host_artifacts)"
    assert_output_path "$BIN_DIR"
    mkdir -p "$BIN_DIR"
    source="$(verified_host_dependency controller "$controller_binary")"
    assert_output_path "$BIN_DIR/$controller_binary"
    [[ ! -d "$BIN_DIR/$controller_binary" ]] || { echo "refusing to overwrite a controller directory: $BIN_DIR/$controller_binary" >&2; return 1; }
    cp -f "$source" "$BIN_DIR/$controller_binary"
}

# ensure_database_layout creates directories but never deletes or replaces user database files.
# ensure_database_layout 只创建目录，绝不删除或替换用户数据库文件。
ensure_database_layout() {
    assert_output_dir
    assert_output_path "$DATABASE_DIR"
    assert_output_path "$DATABASE_DIR/lancedb"
    mkdir -p "$DATABASE_DIR/lancedb"
}

# assert_storage_dependencies performs a read-only preflight so Go compilation cannot leave a partial package.
# assert_storage_dependencies 在 Go 编译前只读预检依赖，避免产生半成品交付包。
assert_storage_dependencies() {
    local profile="$1" sqlite_library lancedb_library controller_binary target
    IFS='|' read -r sqlite_library lancedb_library controller_binary <<< "$(resolve_host_artifacts)"
    if [[ "$profile" == legacy || "$profile" == all ]]; then
        verified_host_dependency sqlite "$sqlite_library" >/dev/null
        verified_host_dependency lancedb "$lancedb_library" >/dev/null
        verified_host_dependency controller "$controller_binary" >/dev/null
    fi
    if [[ "$profile" == native || "$profile" == all ]]; then
        target="$(native_target)"
        resolve_native_artifact "$target" >/dev/null
    fi
}

# do_build compiles Go and synchronizes only the selected dependency profile; it never invokes Cargo.
# do_build 只编译 Go 并同步选定依赖配置，绝不隐式调用 Cargo。
do_build() {
    local build_profile="$1" storage_profile="$2" source_revision source_state_digest release_version ldflags release_args=()
    assert_storage_dependencies "$storage_profile"
    source_revision="$(git -C "$ROOT_DIR" rev-parse HEAD)"
    source_state_digest="$(source_identity)"
    # Keep package names, Git tags, and executable metadata on the same version authority.
    # 保证发行包名称、Git 标签和可执行文件元数据使用同一个版本来源。
    release_version="$(tr -d '\r\n' < "$ROOT_DIR/VERSION")"
    [[ "$release_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]] || { echo "invalid VERSION: $release_version" >&2; return 1; }
    ldflags="-X github.com/openvulcan/vmm/internal/buildinfo.Version=$release_version -X github.com/openvulcan/vmm/internal/buildinfo.SourceRevision=$source_revision -X github.com/openvulcan/vmm/internal/buildinfo.SourceStateDigest=$source_state_digest"
    if [[ "$build_profile" == release ]]; then release_args=(-trimpath -ldflags "$ldflags -s -w"); else release_args=(-ldflags "$ldflags"); fi
    echo "=> Building VMM Gateway ($build_profile, storage=$storage_profile)..."
    # Validate all executable destinations before creating directories or invoking the compiler.
    # 在创建目录或调用编译器前校验全部可执行文件目标。
    assert_output_path "$BIN_DIR"
    local executable_path
    for executable_path in "$EXE_PATH" "$MIGRATE_EXE_PATH" "$TESTER_EXE_PATH"; do
        assert_output_path "$executable_path"
        [[ ! -d "$executable_path" ]] || { echo "refusing to build an executable over a directory: $executable_path" >&2; return 1; }
    done
    mkdir -p "$BIN_DIR"
    go build "${release_args[@]}" -o "$EXE_PATH" "$ROOT_DIR/cmd/vmm-local"
    go build "${release_args[@]}" -o "$MIGRATE_EXE_PATH" "$ROOT_DIR/cmd/vmm-migrate"
    local tester_args=()
    if [[ "$build_profile" == release ]]; then tester_args=(-trimpath -ldflags "-s -w"); fi
    go build "${tester_args[@]}" -o "$TESTER_EXE_PATH" "$ROOT_DIR/cmd/vmm-pii-tester"
    sync_configs
    sync_host_dependencies "$storage_profile"
    IFS='|' read -r _ _ controller_binary <<< "$(resolve_host_artifacts)"
    if [[ "$storage_profile" == legacy || "$storage_profile" == all ]]; then sync_controller_binary; else remove_owned_artifact "$BIN_DIR/$controller_binary"; fi
    ensure_database_layout
    echo "=> Build Success!"
}

# do_clean removes only known build artifacts and leaves database and unknown files untouched.
# do_clean 只删除已知构建产物，并保留数据库和未知文件。
do_clean() {
    [[ -d "$OUTPUT_DIR" ]] || return 0
    assert_output_dir
    remove_owned_configs
    local sqlite_library lancedb_library controller_binary
    IFS='|' read -r sqlite_library lancedb_library controller_binary <<< "$(resolve_host_artifacts)"
    local known=("$EXE_PATH" "$MIGRATE_EXE_PATH" "$TESTER_EXE_PATH" "$BIN_DIR/$controller_binary" "$LIB_DIR/$sqlite_library" "$LIB_DIR/$lancedb_library" "$LIB_DIR/$(native_library_name)" "$LIB_DIR/manifest.json")
    local support_path
    for support_path in "${NATIVE_SUPPORT_ARTIFACT_PATHS[@]}"; do known+=("$LIB_DIR/$support_path"); done
    local path
    for path in "${known[@]}"; do remove_owned_artifact "$path"; done
}

# do_run starts the packaged executable from output/bin for deterministic relative paths.
# do_run 从 output/bin 启动打包程序，确保相对路径稳定。
do_run() {
    [[ -x "$EXE_PATH" ]] || do_build standard "$(resolve_storage_profile)"
    ( cd "$BIN_DIR" && "$EXE_PATH" "${FORWARD_ARGS[@]}" )
}

case "$ACTION" in
    build)
        BUILD_PROFILE="$(resolve_build_profile "${FORWARD_ARGS[@]}")"
        STORAGE_PROFILE="$(resolve_storage_profile)"
        do_build "$BUILD_PROFILE" "$STORAGE_PROFILE"
        ;;
    run) do_run ;;
    clean) do_clean ;;
    *) echo "Usage: $0 {build [release]|run|clean}" >&2; exit 1 ;;
esac
