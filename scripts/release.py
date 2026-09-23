#!/usr/bin/env python3
"""Package verified native builds and publish complete GitHub release sets.
将已验收的原生构建打包，并发布完整的 GitHub 发行集合。
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


# The release matrix is a closed set shared with the workflow's platform names.
# 发行矩阵使用与工作流平台名称一致的封闭集合。
PLATFORMS = {
    "windows-x64": ("windows", "amd64", "x86_64-pc-windows-msvc", "vmm_lancedb_native.dll"),
    "linux-x64": ("linux", "amd64", "x86_64-unknown-linux-gnu", "libvmm_lancedb_native.so"),
    "linux-arm64": ("linux", "arm64", "aarch64-unknown-linux-gnu", "libvmm_lancedb_native.so"),
    "macos-intel": ("darwin", "amd64", "x86_64-apple-darwin", "libvmm_lancedb_native.dylib"),
    "macos-arm64": ("darwin", "arm64", "aarch64-apple-darwin", "libvmm_lancedb_native.dylib"),
}

# Release archives carry every runtime dependency profile so the installer can select storage after download.
# 发行压缩包携带全部运行依赖配置，使安装器可以在下载后再选择存储模式。
RELEASE_MANIFEST_SCHEMA = 2
RELEASE_CAPABILITIES = {
    "schema_version": 1,
    "storage_modes": ["split", "controller", "native", "combined"],
    "combined": {
        "provider": "postgres",
        "flavors": ["standard", "paradedb"],
    },
}

# The external manifest is the protocol-v1 trust boundary consumed by VMMM.
# 外部清单是 VMMM 消费的 protocol-v1 信任边界。
MANIFEST_PROTOCOL_VERSION = 1
RELEASE_MANIFEST_NAME = "manifest.json"
RELEASE_SIGNATURE_NAME = "manifest.sig"
WINDOWS_ATTESTATION_NAME = "windows-authenticode.json"
MANIFEST_PRODUCT = "vmm"
MANIFEST_KEY_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
# This public key is the committed VMM trust root; verification never trusts a downloaded key or CI input.
# 该公钥是提交到仓库的 VMM 信任根；验证绝不信任下载内容或 CI 输入的公钥。
VMM_RELEASE_PUBLIC_KEY_B64 = "h2906GgOkZSmkYGWy3tV6/sH0+zyCcnhTZKjKx06cpM="
WINDOWS_SIGNED_FILES = (
    "bin/vmm-local.exe",
    "bin/vmm-migrate.exe",
    "bin/vmm-pii-tester.exe",
    "bin/vldb-controller.exe",
    "libs/vmm_lancedb_native.dll",
    "libs/vldb_sqlite.dll",
    "libs/vldb_lancedb.dll",
)

# These individual configuration files are reviewed package inputs; unknown files fail the release gate.
# 这些单独配置文件是经过审核的发行输入，未登记的文件会直接阻断发行门禁。
RELEASE_CONFIG_FILES = frozenset(
    {
        "configs/.env.example",
        "configs/bailian.config.example.yaml",
        "configs/base.yaml",
        "configs/config_template/siliconflow.yaml",
        "configs/google_ai_studio.config.example.yaml",
        "configs/native.config.example.yaml",
        "configs/openai.config.example.yaml",
        "configs/openrouter.config.example.yaml",
        "configs/openrouter.scenarios.example.yaml",
    }
)

# These directories contain reviewed, non-secret rule and prompt assets that must stay in every package.
# 这些目录包含经过审核且不含密钥的规则与提示词资产，每个平台发行包都必须保留。
RELEASE_CONFIG_DIRECTORIES = frozenset(
    {
        "configs/noise_rules",
        "configs/pii_rules",
        "configs/prompts",
    }
)

# This project-local overlay is for development and tests, so it must never enter a public release archive.
# 这个项目本地覆盖层只用于开发和测试，绝不能进入公开发行压缩包。
DEVELOPMENT_CONFIG_FILES = frozenset({"configs/config.yaml"})

# Version input must be safe as a Git tag, asset basename and command argument.
# 版本输入必须能安全用于 Git 标签、产物文件名和命令参数。
TAG_PATTERN = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?")


def legacy_artifact_names(platform):
    """Return the platform-specific split/controller dependency names.
    返回平台对应的 split/controller 依赖文件名。
    """
    goos = PLATFORMS[platform][0]
    if goos == "windows":
        return "vldb_sqlite.dll", "vldb_lancedb.dll", "vldb-controller.exe"
    if goos == "linux":
        return "libvldb_sqlite.so", "libvldb_lancedb.so", "vldb-controller"
    if goos == "darwin":
        return "libvldb_sqlite.dylib", "libvldb_lancedb.dylib", "vldb-controller"
    raise ValueError(f"Unsupported release platform OS: {goos}")


def copy_required_file(source, destination, description):
    """Copy one required regular file while rejecting symlinks and directories.
    复制一个必需的普通文件，并拒绝符号链接和目录。
    """
    source = Path(source)
    if source.is_symlink() or not source.is_file():
        raise ValueError(f"Missing or non-regular {description}: {source}")
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)


def stage_runtime_artifacts(output, stage, platform, manifest):
    """Stage Go binaries, legacy libraries, controller, and native support files for one platform.
    暂存一个平台的 Go 二进制、legacy 库、controller 以及原生支持文件。
    """
    output = Path(output)
    stage = Path(stage)
    if not isinstance(manifest, dict):
        raise ValueError("Native manifest must be an object")
    suffix = ".exe" if PLATFORMS[platform][0] == "windows" else ""
    sqlite_library, lancedb_library, controller_binary = legacy_artifact_names(platform)
    native_library = PLATFORMS[platform][3]
    required_binaries = ("vmm-local", "vmm-migrate", "vmm-pii-tester")
    for name in required_binaries:
        copy_required_file(output / "bin" / (name + suffix), stage / "bin" / (name + suffix), f"release binary {name}")
    copy_required_file(output / "bin" / controller_binary, stage / "bin" / controller_binary, "controller binary")

    # The native manifest controls support-file names; each path remains relative to output/libs.
    # 原生清单控制支持文件名称；每个路径都必须保持在 output/libs 内。
    artifact_paths = ["manifest.json", sqlite_library, lancedb_library, native_library]
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list):
        raise ValueError("Native manifest artifacts must be a list")
    artifact_digests = {}
    for item in artifacts:
        if not isinstance(item, dict) or set(item) != {"path", "sha256"}:
            raise ValueError("Native manifest artifact entry must contain path and sha256")
        relative = str(item["path"]).replace("\\", "/")
        relative_path = Path(relative)
        expected_digest = str(item["sha256"]).lower()
        if (
            not relative
            or relative.startswith(("/", "\\"))
            or re.match(r"^[A-Za-z]:", relative)
            or relative_path.is_absolute()
            or ".." in relative_path.parts
        ):
            raise ValueError(f"Unsafe native artifact path: {relative}")
        if not re.fullmatch(r"[0-9a-f]{64}", expected_digest):
            raise ValueError(f"Invalid native artifact digest: {relative}")
        if relative in artifact_digests:
            raise ValueError(f"Duplicate native artifact path: {relative}")
        artifact_digests[relative] = expected_digest
        artifact_paths.append(relative)
    for relative in artifact_paths:
        source = output / "libs" / relative
        if relative in artifact_digests and digest(source) != artifact_digests[relative]:
            raise ValueError(f"Native artifact digest mismatch: {relative}")
        copy_required_file(source, stage / "libs" / relative, f"library artifact {relative}")


def run(*args, env=None):
    """Run an argument-vector command and return stdout, raising on failure.
    通过参数数组执行命令并返回标准输出，失败时抛出错误。
    """
    return subprocess.run(args, check=True, text=True, capture_output=True, env=env).stdout.strip()


def digest(path):
    """Hash one file incrementally and return its SHA-256 hex digest.
    增量读取文件并返回 SHA-256 十六进制摘要。
    """
    with Path(path).open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def validate_identity(tag, commit):
    """Reject unsafe version strings or noncanonical commit IDs before file or API work.
    在文件或接口操作前拒绝不安全版本字符串及非标准提交标识。
    """
    if not TAG_PATTERN.fullmatch(tag) or not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("Expected a version tag and a full lowercase Git commit SHA")


def check_tests(path):
    """Require native and all-profile packaged storage acceptance events to pass without skips.
    要求原生及 all 配置打包存储验收事件全部通过，任何跳过都不能替代验收。
    """
    required = {
        "TestNativeLibraryABIAndErrorBoundary",
        "TestNativeLibraryRoundTrip",
        "TestNativeLibraryPendingSchemaRecovery",
        "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts",
        "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts/legacy-split",
        "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts/legacy-controller",
    }
    passed = set()
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        event = json.loads(line)
        if event.get("Action") == "fail":
            raise ValueError(f"Integration test failure: {event}")
        if event.get("Test") in required:
            if event["Action"] == "skip":
                raise ValueError(f"Required integration test skipped: {event['Test']}")
            if event["Action"] == "pass":
                passed.add(event["Test"])
    if passed != required:
        raise ValueError(f"Missing release acceptance results: {sorted(required - passed)}")


def select_release_configs(tracked_configs):
    """Return the reviewed configuration paths and reject unregistered tracked files.
    返回经过审核的配置路径，并拒绝未登记的已跟踪文件。
    """
    selected = []
    unapproved = []
    for value in sorted({relative.replace("\\", "/") for relative in tracked_configs if relative}):
        if value in DEVELOPMENT_CONFIG_FILES:
            continue
        if value in RELEASE_CONFIG_FILES or any(
            value.startswith(directory + "/") for directory in RELEASE_CONFIG_DIRECTORIES
        ):
            selected.append(value)
        else:
            unapproved.append(value)
    if unapproved:
        raise ValueError(f"Unapproved tracked release config files: {unapproved}")
    if "configs/base.yaml" not in selected:
        raise ValueError("Release config allowlist must include configs/base.yaml")
    return selected


def stage_release_configs(root, stage):
    """Copy only reviewed configuration assets into the package staging directory.
    只将经过审核的配置资产复制到发行包暂存目录。
    """
    tracked_configs = run("git", "-C", str(root), "ls-files", "configs").splitlines()
    release_configs = select_release_configs(tracked_configs)
    for relative in release_configs:
        source = root / Path(relative)
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"Tracked release config is not a regular file: {relative}")
        destination = stage / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
    return release_configs


def package(tag, commit, platform):
    """Validate output metadata, stage owned files, test and archive one platform.
    校验输出元数据，暂存自有文件，验收并压缩一个平台发行包。
    """
    validate_identity(tag, commit)
    root = Path(__file__).resolve().parent.parent
    output = root / "output"
    goos, goarch, target, library = PLATFORMS[platform]
    if (root / "VERSION").read_text(encoding="utf-8").strip() != tag:
        raise ValueError("Release tag differs from VERSION")
    if run("git", "-C", str(root), "rev-parse", "HEAD") != commit:
        raise ValueError("Source checkout differs from the resolved tag commit")
    suffix = ".exe" if goos == "windows" else ""
    for name in ("vmm-local", "vmm-migrate"):
        identity = json.loads(run(str(output / "bin" / (name + suffix)), "-version-json"))
        for key, value in {"version": tag, "source_revision": commit, "goos": goos, "goarch": goarch}.items():
            if identity[key] != value:
                raise ValueError(f"{name}: {key} is {identity[key]!r}, expected {value!r}")
    manifest = json.loads((output / "libs/manifest.json").read_text(encoding="utf-8"))
    if manifest["target"] != target or manifest["library_file"] != library:
        raise ValueError("Native library target does not match release platform")
    if digest(output / "libs" / library) != manifest["library_sha256"]:
        raise ValueError("Native library digest mismatch")

    # Copy only known package content; never distribute output/database or local overrides.
    # 仅复制明确的发行内容，绝不分发 output/database 或本地覆盖配置。
    dist = root / "dist"
    dist.mkdir(exist_ok=True)
    basename = f"vulcan-memory-mesh-{tag}-{platform}"
    with tempfile.TemporaryDirectory(prefix="vmm-release-") as temporary:
        stage = Path(temporary) / basename
        # Every platform archive includes split, controller, native, and combined-mode runtime dependencies.
        # 每个平台压缩包都包含 split、controller、native 与 combined 模式的运行依赖。
        stage_runtime_artifacts(output, stage, platform, manifest)
        # The checkout is the authority for reviewed system assets, not a reused local output tree.
        # 系统资产以当前源码检出中的受控清单为准，避免复用本地 output 或开发覆盖层。
        stage_release_configs(root, stage)
        base = stage / "configs/base.yaml"
        config_text, replaced = re.subn(r'(?m)^(  mode:) "split"$', r'\1 "native"', base.read_text(encoding="utf-8"))
        if replaced != 1:
            raise ValueError("Expected exactly one source storage.mode default")
        base.write_text(config_text, encoding="utf-8")
        for name in ("VERSION", "LICENSE", "README.md"):
            shutil.copy2(root / name, stage / name)
        shutil.copy2(root / "docs/native-storage-guide_CN.md", stage / "NATIVE_STORAGE.md")
        shutil.copy2(root / "docs/github-release-guide_CN.md", stage / "RELEASE_GUIDE.md")
        # Verify the staged default configuration and library layout with a real gRPC process.
        # 使用真实 gRPC 进程验收暂存包的默认配置与动态库布局。
        acceptance_env = dict(os.environ, VMM_NATIVE_PACKAGED_ROOT=str(stage), VMM_PACKAGED_STORAGE_PROFILE="all")
        print(run("go", "test", "./internal/app", "-run", "^TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts$", "-count=1", env=acceptance_env))
        files = sorted(path for path in stage.rglob("*") if path.is_file())
        file_hashes = {path.relative_to(stage).as_posix(): digest(path) for path in files}
        receipt = {
            "manifest_schema": RELEASE_MANIFEST_SCHEMA,
            "version": tag,
            "commit": commit,
            "platform": platform,
            "target": target,
            "storage_mode": "native",
            "storage_profile": "all",
            "capabilities": RELEASE_CAPABILITIES,
            "files": file_hashes,
        }
        (stage / "release-manifest.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        extension = ".zip" if goos == "windows" else ".tar.gz"
        archive = dist / (basename + extension)
        if archive.exists():
            raise ValueError(f"Refusing to overwrite an existing local archive: {archive}")
        if goos == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as handle:
                for path in sorted(stage.rglob("*")):
                    if path.is_file():
                        handle.write(path, path.relative_to(stage.parent))
            with zipfile.ZipFile(archive) as handle:
                if handle.testzip() is not None:
                    raise ValueError("Archive CRC validation failed")
        else:
            with tarfile.open(archive, "w:gz") as handle:
                handle.add(stage, arcname=basename)
            with tarfile.open(archive) as handle:
                for member in handle.getmembers():
                    if member.isfile() and member.name.startswith(basename + "/bin/") and not member.mode & 0o111:
                        raise ValueError(f"Packaged executable lacks execution permission: {member.name}")
        receipt["archive"] = archive.name
        receipt["sha256"] = digest(archive)
        (dist / (basename + ".json")).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        print(f"Packaged {archive.name}: {receipt['sha256']}")


def _reject_duplicate_json_keys(pairs):
    """Reject duplicate JSON object keys before validating release trust metadata.
    在验证发行信任元数据前拒绝 JSON 对象中的重复键。
    """
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"Duplicate JSON key: {key}")
        result[key] = value
    return result


def _load_strict_json(path, description):
    """Load one strict JSON object without accepting duplicate keys or non-objects.
    严格读取一个 JSON 对象，不接受重复键或非对象值。
    """
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"{description} is not a regular file: {path}")
    try:
        value = json.loads(
            path.read_text(encoding="utf-8"),
            object_pairs_hook=_reject_duplicate_json_keys,
        )
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValueError(f"Cannot read strict {description} {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValueError(f"{description} must contain a JSON object")
    return value


def verify_external_manifest_signature(manifest_path, signature_path):
    """Verify the detached VMM signature through the repository Go verifier and fixed public key.
    通过仓库内 Go 验证器和固定公钥校验 VMM 分离签名。
    """
    repo_root = Path(__file__).resolve().parent.parent
    signer = Path(__file__).resolve().with_name("sign_manifest.go")
    if signer.is_symlink() or not signer.is_file():
        raise ValueError(f"Release manifest verifier is missing or not a regular file: {signer}")
    environment = os.environ.copy()
    # Override any caller-provided value so a local or CI environment cannot replace the committed trust root.
    # 覆盖调用方提供的值，防止本地或 CI 环境替换已提交的信任根。
    environment["VMM_RELEASE_ED25519_PUBLIC_KEY"] = VMM_RELEASE_PUBLIC_KEY_B64
    command = [
        "go",
        "run",
        "./scripts/sign_manifest.go",
        "verify",
        "--manifest",
        str(Path(manifest_path).resolve()),
        "--signature",
        str(Path(signature_path).resolve()),
        "--public-key-env",
        "VMM_RELEASE_ED25519_PUBLIC_KEY",
    ]
    try:
        subprocess.run(
            command,
            check=True,
            cwd=repo_root,
            env=environment,
            text=True,
            capture_output=True,
        )
    except FileNotFoundError as exc:
        raise ValueError("Go is required to verify the VMM release signature") from exc
    except subprocess.CalledProcessError as exc:
        detail = (exc.stderr or exc.stdout or "signature verifier failed").strip()
        raise ValueError(f"VMM release Ed25519 verification failed: {detail}") from exc


def refresh_signed_native_manifest(output, platform):
    """Refresh only the native library digest after Authenticode signing changes its bytes.
    Authenticode 签名改变动态库字节后，只刷新原生库摘要并保留清单其余身份字段。
    """
    if platform not in PLATFORMS:
        raise ValueError(f"Unsupported release platform: {platform}")
    if platform != "windows-x64":
        raise ValueError("Signed native manifest refresh is only supported for windows-x64")
    output = Path(output)
    manifest_path = output / "libs" / "manifest.json"
    library_path = output / "libs" / PLATFORMS[platform][3]
    if library_path.is_symlink() or not library_path.is_file():
        raise ValueError(f"Signed native library is missing or not a regular file: {library_path}")
    manifest = _load_strict_json(manifest_path, "native manifest")
    required_fields = {
        "schema_version",
        "abi_version",
        "engine_version",
        "target",
        "source_digest",
        "cargo_lock_sha256",
        "library_file",
        "library_sha256",
        "artifacts",
    }
    if not required_fields.issubset(manifest):
        raise ValueError("Native manifest is missing required identity fields")
    if manifest["target"] != PLATFORMS[platform][2] or manifest["library_file"] != library_path.name:
        raise ValueError("Native manifest identity does not match the signed Windows library")
    actual_digest = digest(library_path)
    if not re.fullmatch(r"[0-9a-f]{64}", str(manifest["library_sha256"])):
        raise ValueError("Native manifest library_sha256 is invalid")
    if manifest["library_sha256"] == actual_digest:
        return actual_digest

    # Replace one field atomically so a partial write cannot become a package input.
    # 原子替换单个字段，避免部分写入成为发行包输入。
    manifest["library_sha256"] = actual_digest
    encoded = (json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    temporary_path = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="wb",
            prefix=".manifest.json.",
            suffix=".tmp",
            dir=manifest_path.parent,
            delete=False,
        ) as temporary:
            temporary_path = Path(temporary.name)
            temporary.write(encoded)
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, manifest_path)
        temporary_path = None
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)
    return actual_digest


def verify_external_manifest(dist, tag, commit, archives):
    """Verify protocol-v1 metadata matches all exact five-platform archive bytes.
    验证 protocol-v1 元数据与五个平台压缩包的精确字节数和摘要一致。
    """
    manifest_path = dist / RELEASE_MANIFEST_NAME
    signature_path = dist / RELEASE_SIGNATURE_NAME
    manifest = _load_strict_json(manifest_path, "release manifest")
    if set(manifest) != {"protocol_version", "product", "tag", "commit", "artifacts"}:
        raise ValueError("Release manifest has an unexpected object shape")
    if manifest["protocol_version"] != MANIFEST_PROTOCOL_VERSION:
        raise ValueError(f"Unsupported release manifest protocol: {manifest['protocol_version']!r}")
    if manifest["product"] != MANIFEST_PRODUCT:
        raise ValueError(f"Unsupported release manifest product: {manifest['product']!r}")
    if manifest["tag"] != tag or manifest["commit"] != commit:
        raise ValueError("Release manifest identity does not match the requested release")
    artifacts = manifest["artifacts"]
    if not isinstance(artifacts, list) or len(artifacts) != len(PLATFORMS):
        raise ValueError("Release manifest must contain exactly five platform artifacts")
    expected_archives = {platform: archive for platform, archive in zip(PLATFORMS, archives)}
    seen = set()
    for item in artifacts:
        if not isinstance(item, dict) or set(item) != {"platform", "filename", "bytes", "sha256"}:
            raise ValueError("Release manifest artifact has an unexpected object shape")
        platform = item["platform"]
        if platform not in expected_archives or platform in seen:
            raise ValueError(f"Release manifest has an invalid or duplicate platform: {platform!r}")
        archive = expected_archives[platform]
        if item["filename"] != archive.name:
            raise ValueError(f"Release manifest filename mismatch: {item['filename']!r}")
        if isinstance(item["bytes"], bool) or not isinstance(item["bytes"], int) or item["bytes"] <= 0:
            raise ValueError(f"Release manifest byte count is invalid for {platform}")
        if item["bytes"] != archive.stat().st_size:
            raise ValueError(f"Release manifest byte count mismatch: {archive.name}")
        expected_digest = digest(archive)
        if not isinstance(item["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", item["sha256"]):
            raise ValueError(f"Release manifest SHA-256 is invalid for {platform}")
        if item["sha256"] != expected_digest:
            raise ValueError(f"Release manifest SHA-256 mismatch: {archive.name}")
        seen.add(platform)
    if seen != set(PLATFORMS):
        raise ValueError(f"Release manifest platform set is incomplete: {sorted(seen)}")

    signature = _load_strict_json(signature_path, "release signature")
    if set(signature) != {"version", "key_id", "signature"}:
        raise ValueError("Release signature has an unexpected object shape")
    if signature["version"] != 1 or not isinstance(signature["key_id"], str) or not MANIFEST_KEY_ID_PATTERN.fullmatch(signature["key_id"]):
        raise ValueError("Release signature envelope has an invalid version or key ID")
    try:
        raw_signature = base64.b64decode(signature["signature"], validate=True)
    except (ValueError, TypeError) as exc:
        raise ValueError("Release signature is not valid Base64") from exc
    if len(raw_signature) != 64:
        raise ValueError("Release signature must contain a 64-byte Ed25519 signature")
    verify_external_manifest_signature(manifest_path, signature_path)
    return [manifest_path, signature_path]


def verify_windows_authenticode(dist, tag, archives):
    """Verify the Windows runner's signed-file attestation against the packaged ZIP.
    将 Windows runner 的签名文件证明与已打包 ZIP 做摘要绑定校验。
    """
    attestation_path = dist / WINDOWS_ATTESTATION_NAME
    attestation = _load_strict_json(attestation_path, "Windows Authenticode attestation")
    expected_archive = dist / f"vulcan-memory-mesh-{tag}-windows-x64.zip"
    if set(attestation) != {"schema_version", "platform", "archive", "archive_sha256", "certificate", "files"}:
        raise ValueError("Windows Authenticode attestation has an unexpected object shape")
    if attestation["schema_version"] != 1 or attestation["platform"] != "windows-x64":
        raise ValueError("Windows Authenticode attestation identity is invalid")
    if attestation["archive"] != expected_archive.name or expected_archive not in archives:
        raise ValueError("Windows Authenticode attestation archive does not match the release")
    if not re.fullmatch(r"[0-9a-f]{64}", str(attestation["archive_sha256"])):
        raise ValueError("Windows Authenticode attestation archive SHA-256 is invalid")
    if attestation["archive_sha256"] != digest(expected_archive):
        raise ValueError("Windows Authenticode attestation archive SHA-256 mismatch")

    expected_subject = os.environ.get("VMM_CERTUM_SUBJECT", "")
    expected_issuer = os.environ.get("VMM_CERTUM_ISSUER", "")
    expected_thumbprint = re.sub(r"\s", "", os.environ.get("VMM_CERTUM_THUMBPRINT", "")).upper()
    if not expected_subject or not expected_issuer or not re.fullmatch(r"[0-9A-F]{40}", expected_thumbprint):
        raise ValueError("VMM_CERTUM_SUBJECT, VMM_CERTUM_ISSUER, and VMM_CERTUM_THUMBPRINT are required")

    certificate = attestation["certificate"]
    if not isinstance(certificate, dict) or set(certificate) != {
        "subject",
        "issuer",
        "thumbprint",
        "chain_status",
        "timestamp_status",
    }:
        raise ValueError("Windows Authenticode certificate attestation has an unexpected shape")
    if (
        not isinstance(certificate["subject"], str)
        or not certificate["subject"].strip()
        or not isinstance(certificate["issuer"], str)
        or not certificate["issuer"].strip()
        or not re.fullmatch(r"[0-9A-Fa-f]{40}", str(certificate["thumbprint"]))
        or certificate["chain_status"] != "Valid"
        or certificate["timestamp_status"] != "Valid"
    ):
        raise ValueError("Windows Authenticode certificate chain or timestamp is not valid")
    if certificate["subject"] != expected_subject or certificate["issuer"] != expected_issuer:
        raise ValueError("Windows Authenticode certificate subject or issuer does not match the configured Certum identity")
    if certificate["thumbprint"].replace(" ", "").upper() != expected_thumbprint:
        raise ValueError("Windows Authenticode certificate thumbprint does not match the configured Certum identity")

    files = attestation["files"]
    if not isinstance(files, list) or len(files) != len(WINDOWS_SIGNED_FILES):
        raise ValueError("Windows Authenticode attestation must contain all required signed files")
    expected_files = set(WINDOWS_SIGNED_FILES)
    seen = set()
    try:
        with zipfile.ZipFile(expected_archive) as archive:
            root = f"vulcan-memory-mesh-{tag}-windows-x64/"
            member_names = archive.namelist()
            names = set(member_names)
            for item in files:
                if not isinstance(item, dict) or set(item) != {"path", "bytes", "sha256", "status"}:
                    raise ValueError("Windows Authenticode file attestation has an unexpected shape")
                relative = item["path"]
                if relative not in expected_files or relative in seen or item["status"] != "Valid":
                    raise ValueError(f"Windows Authenticode file attestation is invalid: {relative!r}")
                if not isinstance(item["bytes"], int) or isinstance(item["bytes"], bool) or item["bytes"] <= 0:
                    raise ValueError(f"Windows Authenticode file byte count is invalid: {relative}")
                if not isinstance(item["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", item["sha256"]):
                    raise ValueError(f"Windows Authenticode file SHA-256 is invalid: {relative}")
                member = root + relative
                if member not in names or member_names.count(member) != 1:
                    raise ValueError(f"Windows archive is missing attested file: {relative}")
                with archive.open(member, "r") as handle:
                    file_digest = hashlib.sha256()
                    file_bytes = 0
                    for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                        file_digest.update(chunk)
                        file_bytes += len(chunk)
                if file_bytes != item["bytes"] or file_digest.hexdigest() != item["sha256"]:
                    raise ValueError(f"Windows Authenticode file digest mismatch: {relative}")
                seen.add(relative)
    except zipfile.BadZipFile as exc:
        raise ValueError(f"Windows release archive is not a valid ZIP: {expected_archive.name}") from exc
    if seen != expected_files:
        raise ValueError(f"Windows Authenticode file set is incomplete: {sorted(seen)}")
    return attestation_path


def verify_assets(dist, tag, commit):
    """Return the exact five verified archives and metadata, refusing incomplete or foreign files.
    返回精确的五平台压缩包和元数据，拒绝缺失或外来文件。
    """
    validate_identity(tag, commit)
    assets = []
    archives = []
    checksums = []
    for platform, (_, _, target, _) in PLATFORMS.items():
        basename = f"vulcan-memory-mesh-{tag}-{platform}"
        metadata = dist / (basename + ".json")
        if metadata.exists() and (metadata.is_symlink() or not metadata.is_file()):
            raise ValueError(f"Release metadata is not a regular file: {metadata.name}")
        receipt = json.loads(metadata.read_text(encoding="utf-8"))
        archive = dist / (basename + (".zip" if platform == "windows-x64" else ".tar.gz"))
        if archive.exists() and (archive.is_symlink() or not archive.is_file()):
            raise ValueError(f"Release archive is not a regular file: {archive.name}")
        expected_fields = {
            "manifest_schema": RELEASE_MANIFEST_SCHEMA,
            "version": tag,
            "commit": commit,
            "platform": platform,
            "target": target,
            "archive": archive.name,
            "storage_mode": "native",
            "storage_profile": "all",
            "capabilities": RELEASE_CAPABILITIES,
        }
        if any(receipt.get(key) != expected for key, expected in expected_fields.items()):
            raise ValueError(f"Release metadata mismatch: {metadata.name}")
        if digest(archive) != receipt["sha256"]:
            raise ValueError(f"Release checksum mismatch: {archive.name}")
        assets.extend((archive, metadata))
        archives.append(archive)
        checksums.extend(f"{digest(path)}  {path.name}" for path in (archive, metadata))
    signed_metadata = verify_external_manifest(dist, tag, commit, archives)
    assets.extend(signed_metadata)
    checksums.extend(f"{digest(path)}  {path.name}" for path in signed_metadata)
    windows_attestation = verify_windows_authenticode(dist, tag, archives)
    assets.append(windows_attestation)
    checksums.append(f"{digest(windows_attestation)}  {windows_attestation.name}")
    expected_names = {path.name for path in assets} | {"SHA256SUMS"}
    if any(path.name not in expected_names for path in dist.iterdir()):
        raise ValueError("Unexpected files in the release asset directory")
    sums = dist / "SHA256SUMS"
    sums.write_text("\n".join(sorted(checksums)) + "\n", encoding="utf-8")
    return assets + [sums]


def publish(tag, commit):
    """Upload a validated five-platform set as a draft, verify downloads, then publish.
    将已校验的五平台产物上传为草稿，验证下载摘要后正式发布。
    """
    dist = Path(__file__).resolve().parent.parent / "dist"
    assets = verify_assets(dist, tag, commit)
    # Capture authenticated bytes before any network mutation; later checks compare against this snapshot.
    # 在任何网络修改前记录已认证文件字节摘要，后续检查必须与该快照一致。
    verified_digests = {asset.name: digest(asset) for asset in assets}
    repo = os.environ["GH_REPO"]
    ref = json.loads(run("gh", "api", f"repos/{repo}/git/ref/tags/{tag}"))["object"]
    if ref["type"] == "tag":
        ref = json.loads(run("gh", "api", f"repos/{repo}/git/tags/{ref['sha']}"))["object"]
    if ref["type"] != "commit" or ref["sha"] != commit:
        raise ValueError("Remote tag moved during the release build")
    existing = subprocess.run(["gh", "api", f"repos/{repo}/releases/tags/{tag}"], text=True, capture_output=True)
    if existing.returncode == 0:
        release = json.loads(existing.stdout)
        if not release["draft"] or release["target_commitish"] != commit:
            raise ValueError("Refusing to modify an existing published or unrelated release")
        if any(asset["name"] not in {path.name for path in assets} for asset in release["assets"]):
            raise ValueError("Draft contains unrelated assets")
    elif "HTTP 404" in existing.stderr:
        notes = dist.parent / "docs/github-release-notes-v0.1.0_CN.md"
        args = ["gh", "release", "create", tag, "--verify-tag", "--target", commit, "--title", f"VulcanMemoryMesh {tag}", "--draft"]
        if tag == "v0.1.0":
            args += ["--notes-file", str(notes)]
        else:
            args += ["--generate-notes"]
        if "-" in tag:
            args += ["--prerelease"]
        run(*args)
    else:
        raise RuntimeError(existing.stderr)
    run("gh", "release", "upload", tag, *map(str, assets), "--clobber")
    # Publication happens only after GitHub serves every uploaded byte with its expected digest.
    # 仅当 GitHub 返回全部上传内容且摘要匹配后，才将草稿转为正式发行版。
    with tempfile.TemporaryDirectory(prefix="vmm-release-download-") as temporary:
        run("gh", "release", "download", tag, "--dir", temporary)
        downloaded = Path(temporary)
        if {path.name for path in downloaded.iterdir()} != {path.name for path in assets}:
            raise ValueError("Uploaded release asset set differs from the verified local set")
        for asset in assets:
            if digest(downloaded / asset.name) != verified_digests[asset.name]:
                raise ValueError(f"Uploaded release digest mismatch: {asset.name}")
    run("gh", "release", "edit", tag, "--draft=false", "--latest=" + ("false" if "-" in tag else "true"))
    print(run("gh", "release", "view", tag, "--json", "url", "--jq", ".url"))


def main():
    """Dispatch explicit package, publish, or acceptance-check operations from CLI arguments.
    根据命令行参数分发明确的打包、发布或验收检查操作。
    """
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)
    for command in ("package", "publish"):
        sub = subcommands.add_parser(command)
        sub.add_argument("--tag", required=True)
        sub.add_argument("--commit", required=True)
        if command == "package":
            sub.add_argument("--platform", choices=PLATFORMS, required=True)
    refresh = subcommands.add_parser("refresh-native-manifest")
    refresh.add_argument("--platform", choices=PLATFORMS, required=True)
    subcommands.add_parser("check-tests").add_argument("log")
    args = parser.parse_args()
    if args.command == "check-tests":
        check_tests(args.log)
    elif args.command == "package":
        package(args.tag, args.commit, args.platform)
    elif args.command == "refresh-native-manifest":
        root = Path(__file__).resolve().parent.parent
        print(refresh_signed_native_manifest(root / "output", args.platform))
    else:
        publish(args.tag, args.commit)


if __name__ == "__main__":
    main()
