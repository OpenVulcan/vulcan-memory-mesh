#!/usr/bin/env bash
# validate_native_artifacts.sh validates one prepared native LanceDB artifact and its manifest.
# validate_native_artifacts.sh 用于校验一份已制备的原生 LanceDB 产物及其清单。
set -euo pipefail

MANIFEST_PATH=""
LIBRARY_PATH=""
TARGET=""
EXPECTED_SOURCE_DIGEST=""
EXPECTED_CARGO_LOCK_SHA256=""
EXPECTED_ENGINE_VERSION="0.39.0"

# Native support paths are a fixed allowlist so a manifest cannot cause arbitrary files to ship.
# 原生支持路径使用固定白名单，避免清单驱动复制任意文件。
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

# parse_args accepts only explicit validator arguments so artifact paths cannot be guessed.
# parse_args 只接受显式校验参数，避免猜测产物路径。
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --manifest) MANIFEST_PATH="$2"; shift 2 ;;
            --library) LIBRARY_PATH="$2"; shift 2 ;;
            --target) TARGET="$2"; shift 2 ;;
            --expected-source-digest) EXPECTED_SOURCE_DIGEST="$2"; shift 2 ;;
            --expected-cargo-lock-sha256) EXPECTED_CARGO_LOCK_SHA256="$2"; shift 2 ;;
            --expected-engine-version) EXPECTED_ENGINE_VERSION="$2"; shift 2 ;;
            *) echo "unknown validator argument: $1" >&2; return 1 ;;
        esac
    done
    [[ -n "$MANIFEST_PATH" && -n "$TARGET" ]] || { echo "usage: $0 --manifest path --target target [--library path]" >&2; return 1; }
}

# sha256_file emits a lowercase SHA-256 digest for one regular file.
# sha256_file 输出一个普通文件的小写 SHA-256 摘要。
sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print tolower($1)}'; return; fi
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print tolower($1)}'; return; fi
    echo "sha256sum or shasum is required" >&2
    return 1
}

# lowercase normalizes hexadecimal values without Bash 4 parameter expansion.
# lowercase 在不依赖 Bash 4 参数展开的情况下规范化十六进制值。
lowercase() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

# expected_library_name maps the target family to the declared native ABI filename.
# expected_library_name 将 target 家族映射到契约声明的原生 ABI 文件名。
expected_library_name() {
    case "$1" in
        x86_64-pc-windows-msvc) printf '%s\n' vmm_lancedb_native.dll ;;
        x86_64-unknown-linux-gnu|aarch64-unknown-linux-gnu) printf '%s\n' libvmm_lancedb_native.so ;;
        x86_64-apple-darwin|aarch64-apple-darwin) printf '%s\n' libvmm_lancedb_native.dylib ;;
        *) echo "unsupported native target: $1" >&2; return 1 ;;
    esac
}

# resolve_artifact_file resolves one safe manifest-relative support path beside the manifest.
# resolve_artifact_file 将安全的清单相对支持路径解析为清单旁文件。
resolve_artifact_file() {
    local manifest_dir="$1" relative="$2"
    [[ "$relative" =~ ^[A-Za-z0-9._/-]+$ && "$relative" != *..* && "$relative" != *\\* && "$relative" != /* ]] || { echo "native support artifact path is unsafe: $relative" >&2; return 1; }
    local candidate="$manifest_dir/$relative"
    [[ -f "$candidate" && ! -L "$candidate" ]] || { echo "native support artifact is missing or a symlink: $candidate" >&2; return 1; }
    printf '%s\n' "$candidate"
}

# validate_support_artifacts checks the fixed support list and every recorded content digest.
# validate_support_artifacts 校验固定支持文件清单以及每个记录的内容摘要。
validate_support_artifacts() {
    local manifest_dir="$1" artifacts_json="$2" expected path digest artifact_path actual index
    artifacts_json="$artifacts_json" python3 - "${NATIVE_SUPPORT_ARTIFACT_PATHS[@]}" <<'PY'
import json, os, sys
value = json.loads(os.environ["artifacts_json"])
expected = sys.argv[1:]
if not isinstance(value, list) or len(value) != len(expected):
    raise SystemExit("native manifest artifacts count mismatch")
seen = set()
for entry in value:
    if not isinstance(entry, dict) or set(entry) != {"path", "sha256"}:
        raise SystemExit("native manifest artifact entry must contain only path and sha256")
    path = entry["path"]
    digest = entry["sha256"]
    if path not in expected:
        raise SystemExit("native manifest contains an unsupported artifact path: " + str(path))
    if path in seen:
        raise SystemExit("native manifest contains a duplicate artifact path: " + path)
    if not isinstance(digest, str) or len(digest) != 64 or any(ch not in "0123456789abcdefABCDEF" for ch in digest):
        raise SystemExit("native support artifact digest is invalid: " + path)
    seen.add(path)
if seen != set(expected):
    raise SystemExit("native manifest is missing support artifact")
PY
    while IFS=$'\t' read -r path digest; do
        artifact_path="$(resolve_artifact_file "$manifest_dir" "$path")"
        actual="$(sha256_file "$artifact_path")"
        [[ "$actual" == "$(lowercase "$digest")" ]] || { echo "native support artifact hash mismatch: $path" >&2; return 1; }
    done < <(artifacts_json="$artifacts_json" python3 -c 'import json,os; [print(str(x["path"])+"\t"+str(x["sha256"])) for x in json.loads(os.environ["artifacts_json"])]')
}

# validate_manifest checks schema, ABI, engine, target, filename, and content hashes as one closed contract.
# validate_manifest 一次性校验 schema、ABI、engine、target、文件名和内容哈希，形成闭合契约。
validate_manifest() {
    [[ -f "$MANIFEST_PATH" && ! -L "$MANIFEST_PATH" ]] || { echo "native manifest is missing or is a symlink: $MANIFEST_PATH" >&2; return 1; }
    [[ "$(basename "$MANIFEST_PATH")" == manifest.json ]] || { echo "native manifest must be named manifest.json" >&2; return 1; }
    local values schema abi engine target source_digest cargo_lock_sha256 library_file library_sha256
    values="$(python3 - "$MANIFEST_PATH" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
for key in ("schema_version", "abi_version", "engine_version", "target", "source_digest", "cargo_lock_sha256", "library_file", "library_sha256"):
    print(json.dumps(value.get(key, ""), ensure_ascii=False))
PY
)"
    local fields=()
    while IFS= read -r field; do fields+=("$field"); done <<< "$values"
    [[ "${#fields[@]}" -eq 8 ]] || { echo "native manifest fields are incomplete" >&2; return 1; }
    schema="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[0]}")"
    abi="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[1]}")"
    engine="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[2]}")"
    target="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[3]}")"
    source_digest="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[4]}")"
    cargo_lock_sha256="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[5]}")"
    library_file="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[6]}")"
    library_sha256="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]))' "${fields[7]}")"
    local artifacts_json
    artifacts_json="$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1], encoding="utf-8")).get("artifacts", ""), ensure_ascii=False))' "$MANIFEST_PATH")"
    [[ "$schema" == 1 && "$abi" == 1 && "$engine" == "$EXPECTED_ENGINE_VERSION" && "$target" == "$TARGET" ]] || { echo "native manifest schema, ABI, engine, or target mismatch" >&2; return 1; }
    [[ "$source_digest" =~ ^[0-9a-fA-F]{64}$ && "$cargo_lock_sha256" =~ ^[0-9a-fA-F]{64}$ && "$library_sha256" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "native manifest digest field is invalid" >&2; return 1; }
    [[ -z "$EXPECTED_SOURCE_DIGEST" || "$(lowercase "$source_digest")" == "$(lowercase "$EXPECTED_SOURCE_DIGEST")" ]] || { echo "native source digest mismatch" >&2; return 1; }
    [[ -z "$EXPECTED_CARGO_LOCK_SHA256" || "$(lowercase "$cargo_lock_sha256")" == "$(lowercase "$EXPECTED_CARGO_LOCK_SHA256")" ]] || { echo "native Cargo.lock digest mismatch" >&2; return 1; }
    [[ "$library_file" == "$(expected_library_name "$TARGET")" && "$library_file" != */* && "$library_file" != *\\* ]] || { echo "native library_file does not match target ABI name" >&2; return 1; }
    validate_support_artifacts "$(dirname "$MANIFEST_PATH")" "$artifacts_json"
    if [[ -z "$LIBRARY_PATH" ]]; then LIBRARY_PATH="$(dirname "$MANIFEST_PATH")/$library_file"; fi
    [[ "$(dirname "$LIBRARY_PATH")" == "$(dirname "$MANIFEST_PATH")" && "$(basename "$LIBRARY_PATH")" == "$library_file" ]] || { echo "native library and manifest must share one directory" >&2; return 1; }
    [[ -f "$LIBRARY_PATH" && ! -L "$LIBRARY_PATH" ]] || { echo "native library is missing or is a symlink: $LIBRARY_PATH" >&2; return 1; }
    local actual_hash
    actual_hash="$(sha256_file "$LIBRARY_PATH")"
    [[ "$actual_hash" == "$(lowercase "$library_sha256")" ]] || { echo "native library hash mismatch: $LIBRARY_PATH" >&2; return 1; }
    printf 'native artifact validated: target=%s; engine=%s; abi=%s; library=%s\n' "$TARGET" "$engine" "$abi" "$library_file"
}

parse_args "$@"
validate_manifest
