#!/bin/bash
set -e

ACTION=${1:-build}

SCRIPT_DIR=$(cd "$(dirname "$0")"; pwd)
ROOT_DIR=$(dirname "$SCRIPT_DIR")
OUTPUT_DIR="$ROOT_DIR/output"
BIN_DIR="$OUTPUT_DIR/bin"
EXE_PATH="$BIN_DIR/vmm-local"

do_clean() {
    echo -e "\033[90m=> 🧹 Cleaning output...\033[0m"
    rm -rf "$OUTPUT_DIR"
}

do_build() {
    echo -e "\033[36m=> 🚀 Building VMM Gateway...\033[0m"
    mkdir -p "$BIN_DIR"
    go build -o "$EXE_PATH" "$ROOT_DIR/cmd/vmm-local/main.go"
    
    echo -e "\033[90m=> 📂 Syncing configs (including prompts)...\033[0m"
    # 递归复制整个 configs 文件夹
    cp -r "$ROOT_DIR/configs" "$OUTPUT_DIR/"
    echo -e "\033[32m=> ✅ Build Success!\033[0m"
}

do_run() {
    if [ ! -f "$EXE_PATH" ]; then do_build; fi
    echo -e "\033[35m=> 🏃 Running VMM...\033[0m"
    
    # 使用括号 ( ) 包裹，确保 cd 只在子进程生效，不影响当前终端环境
    ( cd "$BIN_DIR" && ./vmm-local )
}

case "$ACTION" in
    clean) do_clean ;;
    build) do_build ;;
    run)   do_run ;;
    *)     echo "Usage: $0 {build|run|clean}"; exit 1 ;;
esac