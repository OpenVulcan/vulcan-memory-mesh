#!/bin/bash
set -e

ACTION=${1:-build}
if [ $# -gt 0 ]; then
    shift
fi
FORWARD_ARGS=("$@")

SCRIPT_DIR=$(cd "$(dirname "$0")"; pwd)
ROOT_DIR=$(dirname "$SCRIPT_DIR")
OUTPUT_DIR="$ROOT_DIR/output"
BIN_DIR="$OUTPUT_DIR/bin"
LIB_DIR="$OUTPUT_DIR/libs"
DATABASE_DIR="$OUTPUT_DIR/database"
EXE_PATH="$BIN_DIR/vmm-local"
MIGRATE_EXE_PATH="$BIN_DIR/vmm-migrate"
TESTER_EXE_PATH="$BIN_DIR/vmm-pii-tester"
THIRD_PARTY_DEPS_DIR="$ROOT_DIR/third_party/deps"

do_clean() {
    echo -e "\033[90m=> 🧹 Cleaning output...\033[0m"
    rm -rf "$OUTPUT_DIR"
}

# resolve_host_artifacts returns the SQLite library, LanceDB library, and controller binary names for the current Unix host.
# resolve_host_artifacts 返回当前 Unix 宿主对应的 SQLite 库、LanceDB 库与 controller 二进制名称。
resolve_host_artifacts() {
    case "$(uname -s)" in
        Darwin) printf '%s|%s|%s\n' "libvldb_sqlite.dylib" "libvldb_lancedb.dylib" "vldb-controller" ;;
        Linux) printf '%s|%s|%s\n' "libvldb_sqlite.so" "libvldb_lancedb.so" "vldb-controller" ;;
        *) echo "unsupported platform for VMM packaging: $(uname -s)" >&2; return 1 ;;
    esac
}

# sync_host_dependencies copies pinned direct-mode libraries and the controller binary into the formal package layout.
# sync_host_dependencies 把固定版本 direct 动态库与 controller 二进制复制到正式打包布局。
sync_host_dependencies() {
    if [[ ! -d "$THIRD_PARTY_DEPS_DIR" ]]; then
        echo "missing host dependencies; run scripts/install_host_deps.sh first" >&2
        return 1
    fi
    local sqlite_library
    local lancedb_library
    local controller_binary
    IFS='|' read -r sqlite_library lancedb_library controller_binary <<< "$(resolve_host_artifacts)"
    mkdir -p "$LIB_DIR" "$BIN_DIR"
    for library_name in "$sqlite_library" "$lancedb_library"; do
        if [[ ! -f "$THIRD_PARTY_DEPS_DIR/$library_name" ]]; then
            echo "missing host dynamic library: $THIRD_PARTY_DEPS_DIR/$library_name" >&2
            return 1
        fi
        cp "$THIRD_PARTY_DEPS_DIR/$library_name" "$LIB_DIR/$library_name"
    done
    if [[ ! -f "$THIRD_PARTY_DEPS_DIR/$controller_binary" ]]; then
        echo "missing controller executable: $THIRD_PARTY_DEPS_DIR/$controller_binary" >&2
        return 1
    fi
    cp "$THIRD_PARTY_DEPS_DIR/$controller_binary" "$BIN_DIR/$controller_binary"
    chmod +x "$BIN_DIR/$controller_binary"
}

# sync_runtime_layout refreshes configs and creates the stable database directories shared by split and controller modes.
# sync_runtime_layout 刷新配置，并创建 split 与 controller 模式共享的稳定数据库目录。
sync_runtime_layout() {
    rm -rf "$OUTPUT_DIR/configs"
    cp -r "$ROOT_DIR/configs" "$OUTPUT_DIR/"
    mkdir -p "$DATABASE_DIR/lancedb"
}

do_build() {
    echo -e "\033[36m=> 🚀 Building VMM Gateway...\033[0m"
    mkdir -p "$BIN_DIR"
    # Build the full main package so debug helpers and future entrypoint files are linked into the final binary.
    # 构建完整的 main 包，确保调试辅助文件和后续入口文件都会被链接进最终二进制。
    go build -o "$EXE_PATH" "$ROOT_DIR/cmd/vmm-local"
    go build -o "$MIGRATE_EXE_PATH" "$ROOT_DIR/cmd/vmm-migrate"
    go build -o "$TESTER_EXE_PATH" "$ROOT_DIR/cmd/vmm-pii-tester"

    echo -e "\033[90m=> 📂 Syncing configs (including prompts)...\033[0m"
    sync_runtime_layout
    sync_host_dependencies
    echo -e "\033[32m=> ✅ Build Success!\033[0m"
}

do_run() {
    if [ ! -f "$EXE_PATH" ]; then do_build; fi
    echo -e "\033[35m=> 🏃 Running VMM...\033[0m"
    
    # 使用括号 ( ) 包裹，确保 cd 只在子进程生效，不影响当前终端环境
    ( cd "$BIN_DIR" && ./vmm-local "${FORWARD_ARGS[@]}" )
}

case "$ACTION" in
    clean) do_clean ;;
    build) do_build ;;
    run)   do_run ;;
    *)     echo "Usage: $0 {build|run|clean}"; exit 1 ;;
esac
