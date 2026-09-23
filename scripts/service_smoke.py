"""Exercise the packaged VMM executable through each host's real service manager.
通过各宿主的真实服务管理器验收打包后的 VMM 可执行文件。
"""

import getpass
from contextlib import contextmanager
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
        # Keep service logs under the selected account's data root instead of the package.
        # 将服务日志放在所选账户的数据根中，避免写入程序包。
        lines.extend(["logging:", f"  directory: {json.dumps((data_root / 'logs').as_posix())}"])
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


def wait_for_health(binary, config_root, *, privileged=False):
    """Wait for the real gRPC Healthz path after start or restart.
    启动或重启后等待真实 gRPC Healthz 链路就绪。
    """
    deadline = time.monotonic() + 45
    last_result = ""
    while time.monotonic() < deadline:
        command = [str(binary), "health", "--config", str(config_root), "--json"]
        if privileged:
            # An unrelated CI account cannot read a private directory owned by the selected service account.
            # 无关的 CI 账户不能读取所选服务账户持有的私有配置目录。
            command = ["sudo", "-n", *command]
        result = subprocess.run(
            command,
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


def print_native_diagnostics(name):
    """Print bounded native-manager diagnostics only when this isolated fixture fails.
    仅在隔离夹具失败时打印有界的原生服务管理器诊断。
    """
    commands = []
    if sys.platform.startswith("linux"):
        commands = [
            ["sudo", "-n", "systemctl", "status", f"{name}.service", "--no-pager"],
            ["sudo", "-n", "journalctl", "-u", f"{name}.service", "-n", "120", "--no-pager", "-o", "cat"],
        ]
    elif sys.platform == "darwin":
        commands = [
            ["plutil", "-lint", f"/Library/LaunchDaemons/{name}.plist"],
            ["plutil", "-p", f"/Library/LaunchDaemons/{name}.plist"],
            ["stat", "-f", "%Su %Sp %N", f"/Library/LaunchDaemons/{name}.plist"],
            ["sudo", "-n", "launchctl", "print", f"system/{name}"],
            ["sudo", "-n", "launchctl", "print-disabled", "system"],
            ["sudo", "-n", "log", "show", "--last", "3m", "--style", "compact", "--predicate", f'process == "launchd" AND eventMessage CONTAINS "{name}"'],
        ]
    for command in commands:
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=12, check=False)
            print(f"diagnostic {' '.join(command[:3])} exit={result.returncode}")
            output = result.stdout + result.stderr
            if "print-disabled" in command:
                output = "\n".join(line for line in output.splitlines() if name in line)
            elif "journalctl" in command:
                lines = output.splitlines()
                markers = ("panic:", "fatal error:", "purego:", "symbol not found")
                starts = [index for index, line in enumerate(lines) if any(marker in line for marker in markers)]
                head = "\n".join(lines[max(0, starts[-1] - 3): starts[-1] + 7]) if starts else "\n".join(lines[:10])
                output = head + "\n...\n" + "\n".join(lines[-12:])
            elif "log" in command:
                output = output[-4000:]
            print(output[-6000:])
        except (OSError, subprocess.TimeoutExpired) as error:
            print(f"diagnostic failed: {type(error).__name__}")


@contextmanager
def selected_service_account(root):
    """Use a distinct Linux account for the fixture and restore temporary ownership before cleanup.
    在 Linux 上使用独立账户验收，并在清理临时目录前恢复其文件归属。
    """
    if not sys.platform.startswith("linux"):
        yield getpass.getuser()
        return
    current_user = getpass.getuser()
    service_user = "vmmci" + uuid.uuid4().hex[:12]
    subprocess.run(
        ["sudo", "-n", "useradd", "--system", "--user-group", "--no-create-home", service_user],
        check=True,
        timeout=30,
    )
    try:
        yield service_user
    finally:
        # TemporaryDirectory.cleanup runs as the CI account, so transfer only this fixture back.
        # TemporaryDirectory.cleanup 以 CI 账户运行，因此仅将本次夹具目录归还给它。
        try:
            for name in ("config", "data"):
                path = root / name
                if path.exists():
                    subprocess.run(["sudo", "-n", "chown", "-R", current_user, str(path)], check=True, timeout=30)
        finally:
            subprocess.run(["sudo", "-n", "userdel", service_user], check=True, timeout=30)


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
    with (
        tempfile.TemporaryDirectory(prefix="vmm-service-smoke-") as temporary,
        selected_service_account(Path(temporary).resolve()) as service_user,
    ):
        root = Path(temporary).resolve()
        if sys.platform.startswith("linux"):
            # The separate account needs to traverse the fixture without listing its private contents.
            # 独立账户需要穿越夹具父目录，但无需列出其中的私有内容。
            root.chmod(0o711)
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
        # Check effective values through the actual packaged executable before changing account ownership.
        # 修改账户归属前，通过真实打包程序检查有效配置值。
        effective_output = run_command(binary, ["config", "show-effective", "--config", str(config_root), "--json"])
        effective = json.loads(effective_output)
        if (
            effective["version"] != "v1"
            or effective["redacted"] is not True
            or effective["config"]["embedding"]["model"] != "service-smoke-embedding"
            or effective["config"]["storage"]["mode"] != "split"
            or not effective["config"]["logging"]["format"]
            or "service-smoke-placeholder" in effective_output
        ):
            raise RuntimeError("packaged effective configuration did not preserve layers and redaction")
        if os.name != "nt":
            # The service account must own a private root before log and database writers start.
            # 服务账户在日志和数据库写入器启动前必须拥有私有根目录。
            data_root.mkdir(mode=0o700)
        if sys.platform.startswith("linux"):
            subprocess.run(
                ["sudo", "-n", "chown", "-R", service_user, str(config_root), str(data_root)],
                check=True,
                timeout=30,
            )
        install = ["service", "install", name, "-config", str(config_root), "-auto-start=false"]
        if os.name != "nt":
            install += ["-user", service_user]
        installed = False
        try:
            if service_status(binary, name) != {"state": "not-installed", "auto_start": "false"}:
                raise RuntimeError("missing service was not reported as not-installed")
            run_command(binary, install, privileged=True)
            installed = True
            initial_status = service_status(binary, name)
            if os.name != "nt" and initial_status.get("user") != service_user:
                raise RuntimeError("registered service account differs from the selected local account")
            if initial_status["auto_start"] != "disabled":
                raise RuntimeError("new service did not preserve manual start")
            run_command(binary, ["service", "enable", name], privileged=True)
            if service_status(binary, name)["auto_start"] != "enabled":
                raise RuntimeError("service enable was not persisted")
            run_command(binary, ["service", "disable", name], privileged=True)
            if service_status(binary, name)["auto_start"] != "disabled":
                raise RuntimeError("service disable was not persisted")
            run_command(binary, ["service", "start", name], privileged=True)
            wait_for_health(binary, config_root, privileged=sys.platform.startswith("linux"))
            if service_status(binary, name)["state"] != "running":
                raise RuntimeError("service is not running after start")
            run_command(binary, ["service", "restart", name], privileged=True)
            wait_for_health(binary, config_root, privileged=sys.platform.startswith("linux"))
            run_command(binary, ["service", "stop", name], privileged=True)
            if service_status(binary, name)["state"] != "stopped":
                raise RuntimeError("service is not stopped after stop")
        except Exception:
            print_native_diagnostics(name)
            raise
        finally:
            if installed:
                run_command(binary, ["service", "uninstall", name], privileged=True)
        if service_status(binary, name) != {"state": "not-installed", "auto_start": "false"}:
            raise RuntimeError("removed service was not reported as not-installed")
        run_command(binary, ["service", "uninstall", name], privileged=True)
        if not config_root.is_dir():
            raise RuntimeError("service uninstall removed its configuration")
    print(f"native service lifecycle passed for {sys.platform}")


if __name__ == "__main__":
    main()
