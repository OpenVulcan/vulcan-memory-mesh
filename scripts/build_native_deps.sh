#!/usr/bin/env bash
# build_native_deps.sh is the explicit, locked Cargo preparation step for native LanceDB.
# build_native_deps.sh 是原生 LanceDB 唯一显式执行锁定 Cargo 制备的步骤。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
NATIVE_SOURCE_DIR="$ROOT_DIR/native/lancedb"
NATIVE_DEPS_ROOT="$ROOT_DIR/third_party/deps/native_lancedb"
VALIDATOR_SCRIPT="$SCRIPT_DIR/validate_native_artifacts.sh"
ENGINE_VERSION="0.39.0"
TARGET=""
VERIFY_ONLY=0

# Native support paths pair checked-in inputs with fixed manifest/output paths.
# 原生支持路径将已纳入版本管理的输入与固定清单/输出路径成对声明。
NATIVE_SUPPORT_SOURCE_PATHS=(
    include/vmm_lancedb.h
    licenses/THIRD_PARTY_NOTICES.txt
    licenses/lancedb-0.39.0-license-metadata.txt
    licenses/lancedb-0.39.0-LICENSE
    licenses/jieba-rs-0.10.4-dictionary-notice.txt
    licenses/jieba-rs-0.10.4-LICENSE
    licenses/gse-1.0.2-LICENSE
    licenses/gse-1.0.2-embedded-dictionary-notice.txt
    licenses/cedar-0.30.0-LICENSE
    licenses/modernc-sqlite-1.59.0-LICENSE
    licenses/modernc-libc-1.75.7-LICENSE
    licenses/modernc-mathutil-1.7.1-LICENSE
    licenses/modernc-memory-1.12.1-LICENSE
    licenses/go-humanize-1.0.1-LICENSE
    licenses/go-isatty-0.0.24-LICENSE
    licenses/go-strftime-1.0.0-LICENSE
    licenses/bigfft-20230129092748-LICENSE
    licenses/x-sys-0.47.0-LICENSE
)
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

# parse_args accepts one explicit target or verify-only mode and never infers a cache path.
# parse_args 接受一个显式 target 或只校验模式，绝不猜测缓存路径。
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --target) TARGET="$2"; shift 2 ;;
            --verify-only) VERIFY_ONLY=1; shift ;;
            -h|--help) echo "Usage: $0 [--target TARGET] [--verify-only]"; exit 0 ;;
            *) echo "unknown native dependency argument: $1" >&2; return 1 ;;
        esac
    done
}

# sha256_file emits one lowercase SHA-256 digest.
# sha256_file 输出一个小写 SHA-256 摘要。
sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print tolower($1)}'; return; fi
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print tolower($1)}'; return; fi
    echo "sha256sum or shasum is required" >&2
    return 1
}

# sha256_stream hashes the stable source identity stream used as the cache key.
# sha256_stream 对作为缓存键的稳定源码身份流计算摘要。
sha256_stream() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print tolower($1)}'; return; fi
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 | awk '{print tolower($1)}'; return; fi
    echo "sha256sum or shasum is required" >&2
    return 1
}

# assert_cache_directory keeps native cache writes inside the repository and rejects symlinks at every existing level.
# assert_cache_directory 将原生缓存写入限制在仓库内，并拒绝每一级已存在的符号链接。
assert_cache_directory() {
    local path="$1" create="${2:-0}" relative current part
    case "$path" in "$ROOT_DIR"|"$ROOT_DIR"/*) ;; *) echo "native cache path is outside repository: $path" >&2; return 1 ;; esac
    relative="${path#"$ROOT_DIR"/}"
    current="$ROOT_DIR"
    if [[ "$path" == "$ROOT_DIR" ]]; then relative=""; fi
    if [[ -n "$relative" ]]; then IFS='/' read -r -a cache_parts <<< "$relative"; else cache_parts=(); fi
    for part in "${cache_parts[@]}"; do
        [[ "$part" != "" && "$part" != "." && "$part" != ".." ]] || { echo "invalid native cache path: $path" >&2; return 1; }
        current="$current/$part"
        if [[ -e "$current" || -L "$current" ]]; then
            [[ -d "$current" && ! -L "$current" ]] || { echo "native cache path must be a real directory: $current" >&2; return 1; }
        else
            [[ "$create" -eq 1 ]] || { echo "missing native cache directory: $current" >&2; return 1; }
            mkdir "$current"
        fi
    done
}

# assert_protoc_available reports the protobuf compiler requirement before Cargo starts a long build.
# assert_protoc_available 在 Cargo 长时间构建前明确报告 protobuf 编译器依赖。
assert_protoc_available() {
    if [[ -n "${PROTOC:-}" ]]; then
        [[ -f "$PROTOC" ]] || { echo "PROTOC does not point to a file: $PROTOC" >&2; return 1; }
        return 0
    fi
    # Resolve one platform-specific protoc binary from Cargo's registry cache before using PATH.
    # 在使用 PATH 之前，从 Cargo registry 缓存解析唯一的平台匹配 protoc 文件。
    local cargo_home="${CARGO_HOME:-$HOME/.cargo}" registry_source="$cargo_home/registry/src" package_pattern binary_name vendored_candidates=() candidate
    case "$(uname -s)" in
        Linux) package_pattern='*/protoc-bin-vendored-linux-*/bin/protoc'; binary_name=protoc ;;
        Darwin) package_pattern='*/protoc-bin-vendored-macos-*/bin/protoc'; binary_name=protoc ;;
        MINGW*|MSYS*|CYGWIN*) package_pattern='*/protoc-bin-vendored-win32-*/bin/protoc.exe'; binary_name=protoc.exe ;;
        *) package_pattern=''; binary_name=protoc ;;
    esac
    if [[ -n "$package_pattern" && -d "$registry_source" ]]; then
        while IFS= read -r candidate; do
            [[ -f "$candidate" && ! -L "$candidate" ]] && vendored_candidates+=("$candidate")
        done < <(find "$registry_source" -type f -path "$package_pattern" -print 2>/dev/null | LC_ALL=C sort -u)
        if [[ "${#vendored_candidates[@]}" -gt 1 ]]; then
            echo "multiple vendored protoc binaries found; set PROTOC explicitly: ${vendored_candidates[*]}" >&2
            return 1
        fi
        if [[ "${#vendored_candidates[@]}" -eq 1 ]]; then
            PROTOC="${vendored_candidates[0]}"
            export PROTOC
            echo "using vendored protoc: $PROTOC"
            return 0
        fi
    fi
    command -v protoc >/dev/null 2>&1 || { echo "protoc is required by the locked native dependency graph; install protoc or set PROTOC" >&2; return 1; }
}

# assert_native_support_inputs verifies the checked-in header and notices before Cargo or cache mutation.
# assert_native_support_inputs 在 Cargo 或缓存变更前校验已纳入版本管理的头文件和声明。
assert_native_support_inputs() {
    local source_path
    for source_path in "${NATIVE_SUPPORT_SOURCE_PATHS[@]}"; do
        source_path="$NATIVE_SOURCE_DIR/$source_path"
        [[ -f "$source_path" && ! -L "$source_path" ]] || { echo "missing native support input or symlink: $source_path" >&2; return 1; }
    done
}

# copy_native_support_inputs copies the fixed support set and returns no paths that were not allowlisted.
# copy_native_support_inputs 只复制固定支持文件集，不接受白名单之外的路径。
copy_native_support_inputs() {
    local cache_dir="$1" index source_path destination destination_parent
    for index in "${!NATIVE_SUPPORT_SOURCE_PATHS[@]}"; do
        source_path="$NATIVE_SOURCE_DIR/${NATIVE_SUPPORT_SOURCE_PATHS[$index]}"
        destination="$cache_dir/${NATIVE_SUPPORT_ARTIFACT_PATHS[$index]}"
        destination_parent="$(dirname "$destination")"
        assert_cache_directory "$destination_parent" 1
        if [[ -e "$destination" || -L "$destination" ]]; then
            [[ -f "$destination" && ! -L "$destination" ]] || { echo "native support cache path must be a regular file: $destination" >&2; return 1; }
        fi
        cp -f "$source_path" "$destination"
    done
}

# resolve_target selects the current host target unless a validated cross target is supplied.
# resolve_target 在未指定交叉 target 时选择当前宿主 target，并校验显式值。
resolve_target() {
    if [[ -n "$TARGET" ]]; then
        case "$TARGET" in
            x86_64-pc-windows-msvc|x86_64-unknown-linux-gnu|aarch64-unknown-linux-gnu|x86_64-apple-darwin|aarch64-apple-darwin)
                printf '%s\n' "$TARGET"
                return
                ;;
            *) echo "unsupported native target: $TARGET" >&2; return 1 ;;
        esac
    fi
    local arch="$(uname -m)"
    case "$arch" in x86_64|amd64) arch=x86_64 ;; arm64|aarch64) arch=aarch64 ;; *) echo "unsupported architecture: $arch" >&2; return 1 ;; esac
    case "$(uname -s)" in Linux) printf '%s-unknown-linux-gnu\n' "$arch" ;; Darwin) printf '%s-apple-darwin\n' "$arch" ;; *) echo "unsupported Unix platform" >&2; return 1 ;; esac
}

# native_library_name resolves the exact output filename declared by the ABI contract.
# native_library_name 解析 ABI 契约声明的精确输出文件名。
native_library_name() {
    case "$1" in
        x86_64-pc-windows-msvc) printf '%s\n' vmm_lancedb_native.dll ;;
        x86_64-unknown-linux-gnu|aarch64-unknown-linux-gnu) printf '%s\n' libvmm_lancedb_native.so ;;
        x86_64-apple-darwin|aarch64-apple-darwin) printf '%s\n' libvmm_lancedb_native.dylib ;;
        *) echo "unsupported native target: $1" >&2; return 1 ;;
    esac
}

# source_digest hashes every native source file except generated Cargo target output in stable path order.
# source_digest 按稳定路径顺序对除生成 Cargo target 外的全部原生源码文件计算摘要。
source_digest() {
    local file relative
    while IFS= read -r file; do
        relative="${file#"$NATIVE_SOURCE_DIR"/}"
        printf '%s\0%s\n' "$relative" "$(sha256_file "$file")"
    done < <(find "$NATIVE_SOURCE_DIR" -type f ! -path "$NATIVE_SOURCE_DIR/target/*" ! -path "$NATIVE_SOURCE_DIR/.git/*" -print | LC_ALL=C sort) | sha256_stream
}

# write_manifest writes only the owned manifest file with the locked source and ABI facts.
# write_manifest 只写入本步骤拥有的清单文件，并记录锁定源码与 ABI 事实。
write_manifest() {
    local path="$1" target="$2" rustc_version="$3" source_digest_value="$4" cargo_lock_digest="$5" library_file="$6" library_digest="$7"
    assert_cache_directory "$(dirname "$path")"
    python3 - "$path" "$target" "$rustc_version" "$source_digest_value" "$cargo_lock_digest" "$library_file" "$library_digest" "$ENGINE_VERSION" <<'PY'
import json, os, sys
path, target, rustc_version, source_digest, cargo_lock_sha256, library_file, library_sha256, engine_version = sys.argv[1:]
value = {
    "schema_version": 1,
    "abi_version": 1,
    "engine_version": engine_version,
    "target": target,
    "rustc_version": rustc_version,
    "source_digest": source_digest,
    "cargo_lock_sha256": cargo_lock_sha256,
    "library_file": library_file,
    "library_sha256": library_sha256,
    "artifacts": [
        {"path": relative, "sha256": __import__("hashlib").sha256(open(os.path.join(os.path.dirname(path), relative), "rb").read()).hexdigest()}
        for relative in [
            "native_lancedb-support/include/vmm_lancedb.h",
            "native_lancedb-support/licenses/THIRD_PARTY_NOTICES.txt",
            "native_lancedb-support/licenses/lancedb-0.39.0-license-metadata.txt",
            "native_lancedb-support/licenses/lancedb-0.39.0-LICENSE",
            "native_lancedb-support/licenses/jieba-rs-0.10.4-dictionary-notice.txt",
            "native_lancedb-support/licenses/jieba-rs-0.10.4-LICENSE",
            "native_lancedb-support/licenses/gse-1.0.2-LICENSE",
            "native_lancedb-support/licenses/gse-1.0.2-embedded-dictionary-notice.txt",
            "native_lancedb-support/licenses/cedar-0.30.0-LICENSE",
            "native_lancedb-support/licenses/modernc-sqlite-1.59.0-LICENSE",
            "native_lancedb-support/licenses/modernc-libc-1.75.7-LICENSE",
            "native_lancedb-support/licenses/modernc-mathutil-1.7.1-LICENSE",
            "native_lancedb-support/licenses/modernc-memory-1.12.1-LICENSE",
            "native_lancedb-support/licenses/go-humanize-1.0.1-LICENSE",
            "native_lancedb-support/licenses/go-isatty-0.0.24-LICENSE",
            "native_lancedb-support/licenses/go-strftime-1.0.0-LICENSE",
            "native_lancedb-support/licenses/bigfft-20230129092748-LICENSE",
            "native_lancedb-support/licenses/x-sys-0.47.0-LICENSE",
        ]
    ],
}
temporary = path + ".tmp." + str(__import__("os").getpid())
with open(temporary, "w", encoding="utf-8", newline="\n") as handle:
    json.dump(value, handle, indent=2, ensure_ascii=False)
    handle.write("\n")
__import__("os").replace(temporary, path)
PY
}

# write_current publishes one exact cache selection after the artifact validator succeeds.
# write_current 仅在产物校验成功后发布一份精确缓存选择清单。
write_current() {
    local path="$1" target="$2" source_digest_value="$3" library_file="$4"
    assert_cache_directory "$(dirname "$path")"
    python3 - "$path" "$target" "$source_digest_value" "$library_file" <<'PY'
import json, os, sys
path, target, source_digest, library_file = sys.argv[1:]
value = {"schema_version": 1, "target": target, "source_digest": source_digest, "manifest_path": source_digest + "/manifest.json", "library_file": library_file}
temporary = path + ".tmp." + str(os.getpid())
with open(temporary, "w", encoding="utf-8", newline="\n") as handle:
    json.dump(value, handle, indent=2, ensure_ascii=False)
    handle.write("\n")
os.replace(temporary, path)
PY
}

# verify_current validates the one current selection without invoking Cargo.
# verify_current 只校验 current 选择，不执行 Cargo。
verify_current() {
    local target="$1" target_root="$NATIVE_DEPS_ROOT/$target" current_path="$NATIVE_DEPS_ROOT/$target/current.json"
    assert_cache_directory "$target_root"
    [[ -f "$current_path" ]] || { echo "missing native current.json: $current_path" >&2; return 1; }
    [[ ! -L "$current_path" ]] || { echo "native current.json must not be a symlink: $current_path" >&2; return 1; }
    local values source_digest_value manifest_relative library_file manifest_path
    values="$(python3 - "$current_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
for key in ("schema_version", "target", "source_digest", "manifest_path", "library_file"):
    print(value.get(key, ""))
PY
)"
    local fields=()
    while IFS= read -r field; do fields+=("$field"); done <<< "$values"
    [[ "${fields[0]:-}" == 1 && "${fields[1]:-}" == "$target" && "${fields[2]:-}" =~ ^[0-9a-fA-F]{64} && "${fields[3]:-}" == "${fields[2]:-}/manifest.json" ]] || { echo "native current.json is invalid" >&2; return 1; }
    source_digest_value="${fields[2]}"; manifest_relative="${fields[3]}"; library_file="${fields[4]}"
    manifest_path="$target_root/$manifest_relative"
    [[ -f "$manifest_path" ]] || { echo "missing native manifest: $manifest_path" >&2; return 1; }
    assert_cache_directory "$(dirname "$manifest_path")"
    local library_path="$(dirname "$manifest_path")/$library_file"
    "$VALIDATOR_SCRIPT" --manifest "$manifest_path" --library "$library_path" --target "$target" --expected-source-digest "$source_digest_value"
}

parse_args "$@"
TARGET="$(resolve_target)"
[[ -d "$NATIVE_SOURCE_DIR" ]] || { echo "missing native LanceDB source directory: $NATIVE_SOURCE_DIR" >&2; exit 1; }
[[ -f "$VALIDATOR_SCRIPT" ]] || { echo "missing native artifact validator: $VALIDATOR_SCRIPT" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is required for native manifest handling" >&2; exit 1; }

if [[ "$VERIFY_ONLY" -eq 1 ]]; then
    verify_current "$TARGET"
    echo "native dependency verification succeeded: $TARGET"
    exit 0
fi

CARGO_TOML_PATH="$NATIVE_SOURCE_DIR/Cargo.toml"
CARGO_LOCK_PATH="$NATIVE_SOURCE_DIR/Cargo.lock"
[[ -f "$CARGO_TOML_PATH" && -f "$CARGO_LOCK_PATH" ]] || { echo "native source must contain Cargo.toml and Cargo.lock" >&2; exit 1; }
assert_native_support_inputs
assert_protoc_available
SOURCE_DIGEST="$(source_digest)"
CARGO_LOCK_SHA256="$(sha256_file "$CARGO_LOCK_PATH")"
LIBRARY_FILE="$(native_library_name "$TARGET")"
CARGO_TARGET_DIR="$NATIVE_SOURCE_DIR/target"
echo "native dependency build: target=$TARGET; source_digest=$SOURCE_DIGEST; cargo_lock_sha256=$CARGO_LOCK_SHA256"
cargo build --manifest-path "$CARGO_TOML_PATH" --locked --release --target "$TARGET" --target-dir "$CARGO_TARGET_DIR"
BUILT_LIBRARY_PATH="$CARGO_TARGET_DIR/$TARGET/release/$LIBRARY_FILE"
[[ -f "$BUILT_LIBRARY_PATH" ]] || { echo "Cargo did not produce expected native library: $BUILT_LIBRARY_PATH" >&2; exit 1; }
RUSTC_VERSION="$(rustc --version)"
CACHE_TARGET_DIR="$NATIVE_DEPS_ROOT/$TARGET"
CACHE_DIR="$CACHE_TARGET_DIR/$SOURCE_DIGEST"
assert_cache_directory "$CACHE_DIR" 1
CACHED_LIBRARY_PATH="$CACHE_DIR/$LIBRARY_FILE"
MANIFEST_PATH="$CACHE_DIR/manifest.json"
[[ ! -L "$CACHED_LIBRARY_PATH" && ! -d "$CACHED_LIBRARY_PATH" ]] || { echo "native cached library path must be a regular file: $CACHED_LIBRARY_PATH" >&2; exit 1; }
cp -f "$BUILT_LIBRARY_PATH" "$CACHED_LIBRARY_PATH"
copy_native_support_inputs "$CACHE_DIR"
LIBRARY_SHA256="$(sha256_file "$CACHED_LIBRARY_PATH")"
write_manifest "$MANIFEST_PATH" "$TARGET" "$RUSTC_VERSION" "$SOURCE_DIGEST" "$CARGO_LOCK_SHA256" "$LIBRARY_FILE" "$LIBRARY_SHA256"
"$VALIDATOR_SCRIPT" --manifest "$MANIFEST_PATH" --library "$CACHED_LIBRARY_PATH" --target "$TARGET" --expected-source-digest "$SOURCE_DIGEST" --expected-cargo-lock-sha256 "$CARGO_LOCK_SHA256" --expected-engine-version "$ENGINE_VERSION" >/dev/null
write_current "$CACHE_TARGET_DIR/current.json" "$TARGET" "$SOURCE_DIGEST" "$LIBRARY_FILE"
echo "native dependency ready: $MANIFEST_PATH"
