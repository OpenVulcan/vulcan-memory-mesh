"""Exercise the packaged VMM executable through each host's real service manager.
通过各宿主的真实服务管理器验收打包后的 VMM 可执行文件。
"""

import getpass
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid


def run_command(binary, arguments, *, privileged=False, successful=True):
    """Run a fixed VMM argument vector and return its bounded diagnostic output.
    运行固定的 VMM 参数数组并返回有界诊断输出。
    """
    command = [str(binary), *arguments]
    if privileged and os.name != "nt":
        command = ["sudo", "-n", *command]
    result = subprocess.run(command, capture_output=True, text=True, timeout=90, check=False)
    if successful and result.returncode != 0:
        raise RuntimeError(
            f"{' '.join(arguments[:3])} exited {result.returncode}: "
            f"{(result.stdout + result.stderr)[-3000:]}"
        )
    if not successful and result.returncode == 0:
        raise RuntimeError(f"{' '.join(arguments[:3])} unexpectedly succeeded")
    return result.stdout


def unused_loopback_port():
    """Reserve and release one loopback port for this isolated service fixture.
    为本次隔离服务夹具选择并释放一个回环端口。
    """
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def write_config(config_root, data_root, port):
    """Write a strict offline split-storage override for the packaged runtime.
    为打包运行时写入严格且离线的 split 存储覆盖配置。
    """
    lines = [
        "grpc:",
        f"  listen_addr: {json.dumps(f'127.0.0.1:{port}')}",
        "storage:",
        "  mode: split",
    ]
    # Unix service-account checks require an explicit root owned by the selected user.
    # Unix 服务账户检查要求数据根明确指定且由所选用户拥有。
    if os.name != "nt":
        lines.append(f"  local_data_root: {json.dumps(data_root.as_posix())}")
    lines += [
        "embedding:",
        "  provider: openai",
        "  endpoint: http://127.0.0.1:1/v1",
        "  api_keys: [service-smoke-placeholder]",
        "  model: service-smoke-embedding",
        "  dimension: 3",
        "rerank:",
        "  enabled: false",
        "llm:",
        "  routes:",
        "    - name: service-smoke",
        "      provider: openai",
        "      endpoint: http://127.0.0.1:1/v1",
        "      api_keys: [service-smoke-placeholder]",
        "      model: service-smoke-llm",
        "noise:",
        "  enabled: false",
        "  semantic_enabled: false",
        "management:",
        "  enabled: false",
        "retention:",
        "  enabled: false",
        "",
    ]
    (config_root / "config.yaml").write_text("\n".join(lines), encoding="utf-8")


def service_status(binary, name):
    """Decode the stable native service status without relying on display text.
    解码稳定的原生服务状态，不依赖面向用户的显示文本。
    """
    output = run_command(binary, ["service", "status", name])
    fields = {}
    for line in output.splitlines():
        key, separator, value = line.partition("=")
        if not separator or key in fields:
            raise RuntimeError(f"invalid service status line: {line!r}")
        fields[key] = value
    if "state" not in fields or "auto_start" not in fields:
        raise RuntimeError(f"incomplete service status: {fields!r}")
    return fields


def wait_for_health(binary, config_root):
    """Wait for the real gRPC Healthz path after start or restart.
    启动或重启后等待真实 gRPC Healthz 链路就绪。
    """
    deadline = time.monotonic() + 45
    last_result = ""
    while time.monotonic() < deadline:
        result = subprocess.run(
            [str(binary), "health", "--config", str(config_root), "--json"],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        last_result = result.stdout.strip()
        if result.returncode == 0 and json.loads(last_result).get("status") == "ok":
            return
        time.sleep(1)
    raise RuntimeError(f"service did not become healthy: {last_result[-1000:]}")


def main():
    """Build an isolated registration, exercise its lifecycle, and always remove it.
    创建隔离注册、验收生命周期，并始终清理该服务。
    """
    repository = Path(__file__).resolve().parent.parent
    output_root = repository / "output"
    source_binary = output_root / "bin" / ("vmm-local.exe" if os.name == "nt" else "vmm-local")
    if not source_binary.is_file():
        raise RuntimeError(f"standard packaged executable is missing: {source_binary}")
    name = "VMMCI" + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix="vmm-service-smoke-") as temporary:
        root = Path(temporary).resolve()
        # Recreate the formal package layers without the development-only override config.
        # 复制正式包的目录层，并排除仅用于仓库开发的覆盖配置。
        package_root = root / "package"
        shutil.copytree(output_root / "bin", package_root / "bin")
        shutil.copytree(output_root / "libs", package_root / "libs")
        shutil.copytree(
            output_root / "configs",
            package_root / "configs",
            ignore=shutil.ignore_patterns("config.yaml"),
        )
        binary = package_root / "bin" / source_binary.name
        config_root = root / "config"
        config_root.mkdir(mode=0o700)
        data_root = root / "data"
        write_config(config_root, data_root, unused_loopback_port())
        run_command(binary, ["config", "validate", "--config", str(config_root), "--json"])
        install = ["service", "install", name, "-config", str(config_root), "-auto-start=false"]
        if os.name != "nt":
            install += ["-user", getpass.getuser()]
        installed = False
        try:
            run_command(binary, install, privileged=True)
            installed = True
            if service_status(binary, name)["auto_start"] != "disabled":
                raise RuntimeError("new service did not preserve manual start")
            run_command(binary, ["service", "enable", name], privileged=True)
            if service_status(binary, name)["auto_start"] != "enabled":
                raise RuntimeError("service enable was not persisted")
            run_command(binary, ["service", "disable", name], privileged=True)
            if service_status(binary, name)["auto_start"] != "disabled":
                raise RuntimeError("service disable was not persisted")
            run_command(binary, ["service", "start", name], privileged=True)
            wait_for_health(binary, config_root)
            if service_status(binary, name)["state"] != "running":
                raise RuntimeError("service is not running after start")
            run_command(binary, ["service", "restart", name], privileged=True)
            wait_for_health(binary, config_root)
            run_command(binary, ["service", "stop", name], privileged=True)
            if service_status(binary, name)["state"] != "stopped":
                raise RuntimeError("service is not stopped after stop")
        finally:
            if installed:
                run_command(binary, ["service", "uninstall", name], privileged=True)
        run_command(binary, ["service", "status", name], successful=False)
        if not config_root.is_dir():
            raise RuntimeError("service uninstall removed its configuration")
    print(f"native service lifecycle passed for {sys.platform}")


if __name__ == "__main__":
    main()
